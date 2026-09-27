package engine

import "crypto/rand"

func crandRead(p []byte) (int, error) {
	return rand.Read(p)
}
