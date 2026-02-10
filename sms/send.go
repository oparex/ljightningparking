package sms

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

var key = []byte("passphrasewhichneedstobe32bytes!")

func Send(message string) error {
	cipherText, err := encrypt(key, []byte(message + " " + strconv.Itoa(int(time.Now().Unix()+5))))
	if err != nil {
		return err
	}

	params := url.Values{}
	params.Add("data", string(cipherText))

	resp, err := http.Get(fmt.Sprintf("http://localhost:8080/send"))
	if err != nil {
		return err
	}

	if resp.StatusCode != 200 {
		return errors.New("sending sms failed")
	}

	return nil
}

func encrypt(key, text []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	b := base64.StdEncoding.EncodeToString(text)
	ciphertext := make([]byte, aes.BlockSize+len(b))
	iv := ciphertext[:aes.BlockSize]
	if _, err := io.ReadFull(rand.Reader, iv); err != nil {
		return nil, err
	}
	cfb := cipher.NewCFBEncrypter(block, iv)
	cfb.XORKeyStream(ciphertext[aes.BlockSize:], []byte(b))
	return ciphertext, nil
}
