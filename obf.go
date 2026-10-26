package main

// Деобфускация зашитых строк (см. scripts/obfuscate.py): build-time XOR с
// плавающим ключом + base64. Спасает от strings и статических сигнатурных
// сканеров; от целенаправленной реверс-инженерии не спасает (ключ рядом).

import (
	"encoding/base64"
	"log"
)

func mustDeobf(encB64, keyB64 string) string {
	enc, err := base64.StdEncoding.DecodeString(encB64)
	if err != nil {
		log.Fatal("baked: bad obfuscated data")
	}
	key, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil {
		log.Fatal("baked: bad obfuscation key")
	}
	out := make([]byte, len(enc))
	for i, b := range enc {
		out[i] = b ^ key[i%len(key)]
	}
	return string(out)
}
