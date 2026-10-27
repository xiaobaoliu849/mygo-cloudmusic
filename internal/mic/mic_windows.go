package mic

import (
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"unsafe"
)

// Windows records through the waveform audio of winmm, which goes to the
// microphone chosen in the sound settings, and needs no library but the
// system's.
var (
	winmm         = syscall.NewLazyDLL("winmm.dll")
	waveInOpen    = winmm.NewProc("waveInOpen")
	waveInPrepare = winmm.NewProc("waveInPrepareHeader")
	waveInUnprep  = winmm.NewProc("waveInUnprepareHeader")
	waveInAdd     = winmm.NewProc("waveInAddBuffer")
	waveInStart   = winmm.NewProc("waveInStart")
	waveInReset   = winmm.NewProc("waveInReset")
	waveInClose   = winmm.NewProc("waveInClose")

	kernel32     = syscall.NewLazyDLL("kernel32.dll")
	createEvent  = kernel32.NewProc("CreateEventW")
	waitForEvent = kernel32.NewProc("WaitForSingleObject")
)

const (
	waveMapper    = 0xFFFFFFFF
	callbackEvent = 0x50000
	formatPCM     = 1
	headerDone    = 1
	buffers       = 4
	bufferFrames  = Rate / 20 // 50 ms each
)

type waveFormat struct {
	tag            uint16
	channels       uint16
	samplesPerSec  uint32
	avgBytesPerSec uint32
	blockAlign     uint16
	bitsPerSample  uint16
	size           uint16
}

type waveHeader struct {
	data          uintptr
	bufferLength  uint32
	bytesRecorded uint32
	user          uintptr
	flags         uint32
	loops         uint32
	next          uintptr
	reserved      uintptr
}

func start(got func([]float32)) (func(), error) {
	event, _, err := createEvent.Call(0, 0, 0, 0)
	if event == 0 {
		return nil, fmt.Errorf("mic: %v", err)
	}
	f := waveFormat{tag: formatPCM, channels: 1, samplesPerSec: Rate, avgBytesPerSec: Rate * 2, blockAlign: 2, bitsPerSample: 16}
	var h uintptr
	if r, _, _ := waveInOpen.Call(uintptr(unsafe.Pointer(&h)), waveMapper, uintptr(unsafe.Pointer(&f)), event, 0, callbackEvent); r != 0 {
		syscall.CloseHandle(syscall.Handle(event))
		return nil, mmError(r)
	}
	// The system writes into the buffers and their headers as it records:
	// they stay put until it is done with them.
	var pin runtime.Pinner
	data := make([][]int16, buffers)
	headers := make([]waveHeader, buffers)
	pin.Pin(&headers[0])
	size := unsafe.Sizeof(headers[0])
	for i := range headers {
		data[i] = make([]int16, bufferFrames)
		pin.Pin(&data[i][0])
		headers[i] = waveHeader{data: uintptr(unsafe.Pointer(&data[i][0])), bufferLength: bufferFrames * 2}
		waveInPrepare.Call(h, uintptr(unsafe.Pointer(&headers[i])), size)
		waveInAdd.Call(h, uintptr(unsafe.Pointer(&headers[i])), size)
	}
	if r, _, _ := waveInStart.Call(h); r != 0 {
		waveInReset.Call(h)
		for i := range headers {
			waveInUnprep.Call(h, uintptr(unsafe.Pointer(&headers[i])), size)
		}
		waveInClose.Call(h)
		syscall.CloseHandle(syscall.Handle(event))
		pin.Unpin()
		return nil, mmError(r)
	}

	var mu sync.Mutex
	stopping := false
	done := make(chan struct{})
	go func() {
		defer close(done)
		next := 0 // buffers come back in the order they went
		for {
			waitForEvent.Call(event, 100)
			mu.Lock()
			stop := stopping
			mu.Unlock()
			for headers[next].flags&headerDone != 0 {
				hd := &headers[next]
				n := int(hd.bytesRecorded / 2)
				out := make([]float32, n)
				for i, s := range data[next][:n] {
					out[i] = float32(s) / 32768
				}
				if n > 0 {
					got(out)
				}
				if stop {
					hd.flags &^= headerDone
				} else {
					hd.flags &^= headerDone
					hd.bytesRecorded = 0
					waveInAdd.Call(h, uintptr(unsafe.Pointer(hd)), size)
				}
				next = (next + 1) % buffers
			}
			if stop {
				return
			}
		}
	}()
	return func() {
		// Reset stops recording and gives back the buffers, the last
		// partly filled, marked done, for the goroutine to pass on; one it
		// queued again meanwhile, the second gives back empty.
		waveInReset.Call(h)
		mu.Lock()
		stopping = true
		mu.Unlock()
		<-done
		waveInReset.Call(h)
		for i := range headers {
			waveInUnprep.Call(h, uintptr(unsafe.Pointer(&headers[i])), size)
		}
		waveInClose.Call(h)
		syscall.CloseHandle(syscall.Handle(event))
		pin.Unpin()
	}, nil
}

// mmError says what a waveIn error code means, where it is a common one.
func mmError(r uintptr) error {
	switch r {
	case 2: // MMSYSERR_BADDEVICEID
		return fmt.Errorf("mic: no microphone")
	case 4: // MMSYSERR_ALLOCATED
		return fmt.Errorf("mic: the microphone is in use")
	case 6: // MMSYSERR_NODRIVER
		return fmt.Errorf("mic: no microphone driver")
	case 32: // WAVERR_BADFORMAT
		return fmt.Errorf("mic: the microphone cannot record at %d Hz", Rate)
	}
	return fmt.Errorf("mic: waveIn error %d", r)
}
