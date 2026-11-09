//go:build !darwin && !linux

package keystore

type none struct{}

func system() Store { return none{} }

func (none) Name() string               { return "none" }
func (none) Get(string) ([]byte, error) { return nil, ErrUnavailable }
func (none) Set(string, []byte) error   { return ErrUnavailable }
func (none) Delete(string) error        { return ErrUnavailable }
