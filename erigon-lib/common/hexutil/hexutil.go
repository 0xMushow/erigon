// Copyright 2024 The Erigon Authors
// This file is part of Erigon.
//
// Erigon is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// Erigon is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with Erigon. If not, see <http://www.gnu.org/licenses/>.

package hexutil

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"
	"strconv"
)

const uintBits = 32 << (uint64(^uint(0)) >> 63)

// Decode decodes a hex string with 0x prefix.
func Decode(input string) ([]byte, error) {
	if len(input) == 0 {
		return nil, ErrEmptyString
	}
	if !has0xPrefix(input) {
		return nil, ErrMissingPrefix
	}
	b, err := hex.DecodeString(input[2:])
	if err != nil {
		return nil, mapError(err)
	}
	return b, nil
}

// MustDecode decodes a hex string with 0x prefix. It panics for invalid input.
func MustDecode(input string) []byte {
	dec, err := Decode(input)
	if err != nil {
		panic(err)
	}
	return dec
}

// DecodeUint64 decodes a hex string with 0x prefix as a quantity.
func DecodeUint64(input string) (uint64, error) {
	raw, err := checkNumber(input)
	if err != nil {
		return 0, err
	}
	dec, err := strconv.ParseUint(raw, 16, 64)
	if err != nil {
		return 0, mapError(err)
	}
	return dec, nil
}

// EncodeUint64 encodes i as a hex string with 0x prefix.
func EncodeUint64(i uint64) string {
	enc := make([]byte, 2, 10)
	copy(enc, "0x")
	return string(strconv.AppendUint(enc, i, 16))
}

var bigWordNibbles int

func init() {
	// This is a weird way to compute the number of nibbles required for big.Word.
	// The usual way would be to use constant arithmetic but go vet can't handle that.
	b, _ := new(big.Int).SetString("FFFFFFFFFF", 16)
	switch len(b.Bits()) {
	case 1:
		bigWordNibbles = 16
	case 2:
		bigWordNibbles = 8
	default:
		panic("weird big.Word size")
	}
}

// DecodeBig decodes a hex string with 0x prefix as a quantity.
// Numbers larger than 256 bits are not accepted.
func DecodeBig(input string) (*big.Int, error) {
	raw, err := checkNumber(input)
	if err != nil {
		return nil, err
	}
	if len(raw) > 64 {
		return nil, ErrBig256Range
	}
	words := make([]big.Word, len(raw)/bigWordNibbles+1)
	end := len(raw)
	for i := range words {
		start := end - bigWordNibbles
		if start < 0 {
			start = 0
		}
		for ri := start; ri < end; ri++ {
			nib := decodeNibble(raw[ri])
			if nib == badNibble {
				return nil, ErrSyntax
			}
			words[i] *= 16
			words[i] += big.Word(nib)
		}
		end = start
	}
	dec := new(big.Int).SetBits(words)
	return dec, nil
}

// MustDecodeBig decodes a hex string with 0x prefix as a quantity.
// It panics for invalid input.
func MustDecodeBig(input string) *big.Int {
	dec, err := DecodeBig(input)
	if err != nil {
		panic(err)
	}
	return dec
}

// EncodeBig encodes bigint as a hex string with 0x prefix.
// The sign of the integer is ignored.
func EncodeBig(bigint *big.Int) string {
	nbits := bigint.BitLen()
	if nbits == 0 {
		return "0x0"
	}
	return fmt.Sprintf("%#x", bigint)
}

func has0xPrefix(input string) bool {
	return len(input) >= 2 && input[0] == '0' && (input[1] == 'x' || input[1] == 'X')
}

// IsValidQuantity checks if input is a valid hex-encoded quantity as per JSON-RPC spec.
// It returns nil if the input is valid, otherwise an error.
func IsValidQuantity(input string) error {
	input, err := checkNumber(input)
	if err != nil {
		return err
	}
	if len(input) > 64 {
		return ErrTooBigHexString
	}
	for _, b := range input {
		if decodeNibble(byte(b)) == badNibble {
			return ErrHexStringInvalid
		}
	}
	return nil
}

func checkNumber(input string) (raw string, err error) {
	if len(input) == 0 {
		return "", ErrEmptyString
	}
	if !has0xPrefix(input) {
		return "", ErrMissingPrefix
	}
	input = input[2:]
	if len(input) == 0 {
		return "", ErrEmptyNumber
	}
	if len(input) > 1 && input[0] == '0' {
		return "", ErrLeadingZero
	}
	return input, nil
}

// ignore these errors to keep compatibility with go ethereum
// nolint:errorlint
func mapError(err error) error {
	if err, ok := err.(*strconv.NumError); ok {
		switch err.Err {
		case strconv.ErrRange:
			return ErrUint64Range
		case strconv.ErrSyntax:
			return ErrSyntax
		}
	}
	if _, ok := err.(hex.InvalidByteError); ok {
		return ErrSyntax
	}
	if err == hex.ErrLength {
		return ErrOddLength
	}
	return err
}

// CompressNibbles - supports only even number of nibbles
//
// HI_NIBBLE(b) = (b >> 4) & 0x0F
// LO_NIBBLE(b) = b & 0x0F
func CompressNibbles(nibbles []byte, out *[]byte) {
	tmp := (*out)[:0]
	for i := 0; i < len(nibbles); i += 2 {
		tmp = append(tmp, nibbles[i]<<4|nibbles[i+1])
	}
	*out = tmp
}

// DecompressNibbles - supports only even number of nibbles
//
// HI_NIBBLE(b) = (b >> 4) & 0x0F
// LO_NIBBLE(b) = b & 0x0F
func DecompressNibbles(in []byte, out *[]byte) {
	tmp := (*out)[:0]
	for i := 0; i < len(in); i++ {
		tmp = append(tmp, (in[i]>>4)&0x0F, in[i]&0x0F)
	}
	*out = tmp
}

func MustDecodeHex(in string) []byte {
	in = strip0x(in)
	if len(in)%2 == 1 {
		in = "0" + in
	}
	payload, err := hex.DecodeString(in)
	if err != nil {
		panic(err)
	}
	return payload
}

func strip0x(str string) string {
	if len(str) >= 2 && str[0] == '0' && (str[1] == 'x' || str[1] == 'X') {
		return str[2:]
	}
	return str
}

// EncodeTs encodes a TimeStamp (BlockNumber or TxNumber or other uin64) as big endian
func EncodeTs(number uint64) []byte {
	var enc [8]byte
	binary.BigEndian.PutUint64(enc[:], number)
	return enc[:]
}

// Encode encodes b as a hex string with 0x prefix.
func Encode(b []byte) string {
	enc := make([]byte, len(b)*2+2)
	copy(enc, "0x")
	hex.Encode(enc[2:], b)
	return string(enc)
}

func FromHex(s string) []byte {
	if Has0xPrefix(s) {
		s = s[2:]
	}
	if len(s)%2 == 1 {
		s = "0" + s
	}
	return Hex2Bytes(s)
}

// Has0xPrefix validates str begins with '0x' or '0X'.
func Has0xPrefix(str string) bool {
	return len(str) >= 2 && str[0] == '0' && (str[1] == 'x' || str[1] == 'X')
}

// Hex2Bytes returns the bytes represented by the hexadecimal string str.
func Hex2Bytes(str string) []byte {
	h, _ := hex.DecodeString(str)
	return h
}

// IsHex validates whether each byte is valid hexadecimal string.
func IsHex(str string) bool {
	if len(str)%2 != 0 {
		return false
	}
	for _, c := range []byte(str) {
		if !isHexCharacter(c) {
			return false
		}
	}
	return true
}

// isHexCharacter returns bool of c being a valid hexadecimal.
func isHexCharacter(c byte) bool {
	return ('0' <= c && c <= '9') || ('a' <= c && c <= 'f') || ('A' <= c && c <= 'F')
}

func MustDecodeString(s string) []byte {
	r, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return r
}
