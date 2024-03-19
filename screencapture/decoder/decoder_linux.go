package decoder

import (
	"context"
	"errors"
)

type Decoder struct {
	ctx        context.Context
	address    string
	portNumber int
}

func (d *Decoder) Decode(outputFilename string) (err error) {
	return errors.New("not implemented")
}

func (d *Decoder) cancel() {
	return
}
