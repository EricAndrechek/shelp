package term

// ReadKeys reads keys from fd, which must be in raw mode (MakeRaw), and
// sends them to out until stop is closed or a read fails. Raw mode's reads
// return within 100ms, so ReadKeys never leaves a read blocked on the
// terminal after stop: whatever reads the terminal next gets every byte.
// A lone Esc is reported once the input goes quiet after it.
func ReadKeys(fd int, stop <-chan struct{}, out chan<- Key) error {
	var d Decoder
	buf := make([]byte, 256)
	for {
		select {
		case <-stop:
			return nil
		default:
		}
		n, err := read(fd, buf)
		if err != nil {
			return err
		}
		var keys []Key
		if n == 0 {
			keys = d.Timeout()
		} else {
			keys = d.Feed(buf[:n])
		}
		for _, k := range keys {
			select {
			case out <- k:
			case <-stop:
				return nil
			}
		}
	}
}

// ReadKey reads one key from fd (in raw mode). ok is false when stop
// closed first.
func ReadKey(fd int, stop <-chan struct{}) (k Key, ok bool, err error) {
	keys := make(chan Key, 16)
	quit := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- ReadKeys(fd, quit, keys) }()
	defer func() { close(quit); <-done }()
	select {
	case k := <-keys:
		return k, true, nil
	case err := <-done:
		done <- err // for the deferred receive
		return Key{}, false, err
	case <-stop:
		return Key{}, false, nil
	}
}
