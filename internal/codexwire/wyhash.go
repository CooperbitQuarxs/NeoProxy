// Package codexwire ports NeoPi's OpenAI Codex request shaping and transport
// identity so upstream Codex sees the same requests NeoPi sends.
package codexwire

import (
	"encoding/binary"
	"math/bits"
	"strconv"
)

var wyhashSecret = [4]uint64{0xa0761d6478bd642f, 0xe7037ed1a0b428db, 0x8ebc6af09c88c6e3, 0x589965cc75374cc3}

func wyMum(a, b uint64) (uint64, uint64) {
	hi, lo := bits.Mul64(a, b)
	return lo, hi
}

func wyMix(a, b uint64) uint64 {
	lo, hi := wyMum(a, b)
	return lo ^ hi
}

// bunHash is Bun.hash: Zig's one-shot Wyhash with seed zero.
func bunHash(input []byte) uint64 {
	var state [3]uint64
	state[0] = wyMix(wyhashSecret[0], wyhashSecret[1])
	state[1], state[2] = state[0], state[0]
	var a, b uint64
	read8 := func(p []byte) uint64 { return binary.LittleEndian.Uint64(p) }
	read4 := func(p []byte) uint64 { return uint64(binary.LittleEndian.Uint32(p)) }
	n := len(input)
	if n <= 16 {
		switch {
		case n >= 4:
			end := n - 4
			quarter := (n >> 3) << 2
			a = read4(input)<<32 | read4(input[quarter:])
			b = read4(input[end:])<<32 | read4(input[end-quarter:])
		case n > 0:
			a = uint64(input[0])<<16 | uint64(input[n>>1])<<8 | uint64(input[n-1])
		}
	} else {
		i := 0
		if n >= 48 {
			for ; i+48 < n; i += 48 {
				for lane := 0; lane < 3; lane++ {
					x := read8(input[i+16*lane:])
					y := read8(input[i+16*lane+8:])
					state[lane] = wyMix(x^wyhashSecret[lane+1], y^state[lane])
				}
			}
			state[0] ^= state[1] ^ state[2]
		}
		rest := input[i:]
		for j := 0; j+16 < len(rest); j += 16 {
			state[0] = wyMix(read8(rest[j:])^wyhashSecret[1], read8(rest[j+8:])^state[0])
		}
		a = read8(input[n-16:])
		b = read8(input[n-8:])
	}
	a ^= wyhashSecret[1]
	b ^= state[0]
	a, b = wyMum(a, b)
	return wyMix(a^wyhashSecret[0]^uint64(n), b^wyhashSecret[1])
}

// bunHash36 is Bun.hash(value).toString(36) for a JavaScript string's UTF-8 bytes.
func bunHash36(value string) string {
	return strconv.FormatUint(bunHash([]byte(value)), 36)
}
