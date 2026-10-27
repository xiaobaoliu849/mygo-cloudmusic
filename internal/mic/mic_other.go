//go:build !windows

package mic

func start(got func([]float32)) (func(), error) { return nil, ErrUnsupported }
