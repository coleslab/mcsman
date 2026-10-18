package server

import (
	"fmt"
	"net"
	"strconv"
)

// checkPortsFree verifies nothing else is listening on the given TCP ports by
// briefly binding each one.
func checkPortsFree(ports ...int) error {
	for _, p := range ports {
		ln, err := net.Listen("tcp", ":"+strconv.Itoa(p))
		if err != nil {
			return fmt.Errorf("%w: port %d is already in use", ErrConflict, p)
		}
		ln.Close()
	}
	return nil
}
