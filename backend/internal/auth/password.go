package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"runtime/debug"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	memory      uint32 = 64 * 1024
	iterations  uint32 = 2
	parallelism uint8  = 2
	saltLength         = 16
	keyLength   uint32 = 32
)

// releaseScratch hands argon2id's working set back to the OS.
//
// argon2.IDKey derives the key in one `memory`-KiB allocation (64 MiB with the
// parameters above) and returns only the 32-byte key, so the scratch block is
// garbage the moment the hash finishes. The Go runtime keeps that garbage as
// free spans and never returns it to the kernel on its own, which leaves a
// 130 MB container RSS on a box that is otherwise idle. Logins are rare, so
// one scavenge per hash costs far less than sitting on the 64 MiB forever.
func releaseScratch() {
	debug.FreeOSMemory()
}

func HashPassword(password string) (string, error) {
	if len(password) < 8 {
		return "", fmt.Errorf("密码至少需要 8 个字符")
	}
	defer releaseScratch()
	salt := make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, iterations, memory, parallelism, keyLength)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		memory, iterations, parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

func VerifyPassword(password, encoded string) bool {
	defer releaseScratch()
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var mem uint32
	var iter uint32
	var par uint8
	for _, p := range strings.Split(parts[3], ",") {
		kv := strings.SplitN(p, "=", 2)
		if len(kv) != 2 {
			return false
		}
		n, err := strconv.ParseUint(kv[1], 10, 32)
		if err != nil {
			return false
		}
		switch kv[0] {
		case "m":
			mem = uint32(n)
		case "t":
			iter = uint32(n)
		case "p":
			par = uint8(n)
		}
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, iter, mem, par, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}
