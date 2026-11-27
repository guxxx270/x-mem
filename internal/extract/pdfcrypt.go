package extract

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rc4"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/binary"
	"errors"
)

// Many PDFs are encrypted only to restrict printing or copying: they open
// without a password, and their text can be read with the empty user
// password. Files that need a password to open are reported as such.

var errPDFPassword = errors.New("PDF 有打开密码，读不了")

var pdfPad = []byte{
	0x28, 0xBF, 0x4E, 0x5E, 0x4E, 0x75, 0x8A, 0x41, 0x64, 0x00, 0x4E, 0x56, 0xFF, 0xFA, 0x01, 0x08,
	0x2E, 0x2E, 0x00, 0xB6, 0xD0, 0x68, 0x3E, 0x80, 0x2F, 0x0C, 0xA9, 0xFE, 0x64, 0x53, 0x69, 0x7A,
}

type pdfCrypt struct {
	key    []byte
	method string // "rc4", "aes", "aes256" or "" (identity)
}

func newPDFCrypt(enc pdfDict, id0 []byte) (*pdfCrypt, error) {
	if name(enc["Filter"]) != "Standard" {
		return nil, errors.New("PDF 用了非标准的加密方式，读不了")
	}
	v, r := num(enc["V"]), num(enc["R"])
	o, _ := enc["O"].(pdfString)
	u, _ := enc["U"].(pdfString)
	p := int32(num(enc["P"]))
	if f, ok := enc["P"].(float64); ok {
		p = int32(int64(f))
	}
	method := "rc4"
	if v >= 4 {
		cf, _ := enc["CF"].(pdfDict)
		stm := name(enc["StmF"])
		if stm == "" || stm == "Identity" {
			method = ""
		} else if cfd, ok := cf[stm].(pdfDict); ok {
			switch name(cfd["CFM"]) {
			case "AESV2":
				method = "aes"
			case "AESV3":
				method = "aes256"
			case "None":
				method = ""
			}
		}
	}
	c := &pdfCrypt{method: method}
	if r >= 5 {
		ue, _ := enc["UE"].(pdfString)
		if len(u) < 48 || len(ue) < 32 {
			return nil, errPDFPassword
		}
		var check, ikey []byte
		if r == 5 {
			h := sha256.Sum256(u[32:40])
			check = h[:]
			k := sha256.Sum256(u[40:48])
			ikey = k[:]
		} else {
			check = hash2B(nil, u[32:40], nil)
			ikey = hash2B(nil, u[40:48], nil)
		}
		if !bytes.Equal(check, u[:32]) {
			return nil, errPDFPassword
		}
		block, err := aes.NewCipher(ikey)
		if err != nil {
			return nil, err
		}
		key := make([]byte, 32)
		cipher.NewCBCDecrypter(block, make([]byte, 16)).CryptBlocks(key, ue[:32])
		c.key, c.method = key, "aes256"
		return c, nil
	}
	n := 5
	if r >= 3 {
		if l := num(enc["Length"]); l >= 40 && l <= 128 {
			n = l / 8
		}
	}
	h := md5.New()
	h.Write(pdfPad)
	if len(o) >= 32 {
		h.Write(o[:32])
	}
	var pb [4]byte
	binary.LittleEndian.PutUint32(pb[:], uint32(p))
	h.Write(pb[:])
	h.Write(id0)
	if r >= 4 {
		if em, ok := enc["EncryptMetadata"].(bool); ok && !em {
			h.Write([]byte{0xFF, 0xFF, 0xFF, 0xFF})
		}
	}
	key := h.Sum(nil)
	if r >= 3 {
		for i := 0; i < 50; i++ {
			s := md5.Sum(key[:n])
			key = s[:]
		}
	}
	key = key[:n]
	// Check the empty user password against U.
	var ok bool
	if r == 2 {
		out := make([]byte, 32)
		rc, _ := rc4.NewCipher(key)
		rc.XORKeyStream(out, pdfPad)
		ok = len(u) >= 32 && bytes.Equal(out, u[:32])
	} else {
		hh := md5.New()
		hh.Write(pdfPad)
		hh.Write(id0)
		out := hh.Sum(nil)
		for i := 0; i < 20; i++ {
			k := make([]byte, len(key))
			for j := range key {
				k[j] = key[j] ^ byte(i)
			}
			rc, _ := rc4.NewCipher(k)
			rc.XORKeyStream(out, out)
		}
		ok = len(u) >= 16 && bytes.Equal(out[:16], u[:16])
	}
	if !ok {
		return nil, errPDFPassword
	}
	c.key = key
	return c, nil
}

// hash2B is ISO 32000-2's algorithm 2.B (revision 6), for the empty
// password.
func hash2B(password, salt, udata []byte) []byte {
	h := sha256.Sum256(append(append(append([]byte{}, password...), salt...), udata...))
	k := h[:]
	for i := 0; ; i++ {
		seq := append(append(append([]byte{}, password...), k...), udata...)
		k1 := bytes.Repeat(seq, 64)
		block, _ := aes.NewCipher(k[:16])
		e := make([]byte, len(k1))
		cipher.NewCBCEncrypter(block, k[16:32]).CryptBlocks(e, k1)
		sum := 0
		for _, b := range e[:16] {
			sum += int(b)
		}
		switch sum % 3 {
		case 0:
			s := sha256.Sum256(e)
			k = s[:]
		case 1:
			s := sha512.Sum384(e)
			k = s[:]
		default:
			s := sha512.Sum512(e)
			k = s[:]
		}
		if i >= 63 && int(e[len(e)-1]) <= i-31 {
			break
		}
	}
	return k[:32]
}

func (c *pdfCrypt) decrypt(numObj, gen int, data []byte, _ bool) []byte {
	switch c.method {
	case "":
		return data
	case "aes256":
		return aesCBC(c.key, data)
	}
	h := md5.New()
	h.Write(c.key)
	h.Write([]byte{byte(numObj), byte(numObj >> 8), byte(numObj >> 16), byte(gen), byte(gen >> 8)})
	if c.method == "aes" {
		h.Write([]byte("sAlT"))
	}
	k := h.Sum(nil)
	if n := len(c.key) + 5; n < 16 {
		k = k[:n]
	}
	if c.method == "aes" {
		return aesCBC(k, data)
	}
	out := make([]byte, len(data))
	rc, err := rc4.NewCipher(k)
	if err != nil {
		return data
	}
	rc.XORKeyStream(out, data)
	return out
}

func aesCBC(key, data []byte) []byte {
	if len(data) < 32 || len(data)%16 != 0 {
		return nil
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil
	}
	out := make([]byte, len(data)-16)
	cipher.NewCBCDecrypter(block, data[:16]).CryptBlocks(out, data[16:])
	if p := int(out[len(out)-1]); p > 0 && p <= 16 && p <= len(out) {
		out = out[:len(out)-p]
	}
	return out
}
