package jsengine

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// javaExtras 提供上游 JsExtensions 里最常用的一批纯计算能力
// （base64 / 摘要 / hex / URI / AES / UUID / 时间格式化）。
// 依赖宿主环境的能力（文件、图片、WebView）不在这里实现。
func javaExtras() map[string]any {
	return map[string]any{
		"base64Encode": func(s string) string {
			return base64.StdEncoding.EncodeToString([]byte(s))
		},
		"base64Decode": func(s string) string {
			b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
			if err != nil {
				b, err = base64.RawStdEncoding.DecodeString(strings.TrimSpace(s))
				if err != nil {
					return ""
				}
			}
			return string(b)
		},
		"md5Encode": func(s string) string {
			sum := md5.Sum([]byte(s))
			return hex.EncodeToString(sum[:])
		},
		"md5Encode16": func(s string) string {
			sum := md5.Sum([]byte(s))
			return hex.EncodeToString(sum[:])[8:24]
		},
		"sha1Encode": func(s string) string {
			sum := sha1.Sum([]byte(s))
			return hex.EncodeToString(sum[:])
		},
		"sha256Encode": func(s string) string {
			sum := sha256.Sum256([]byte(s))
			return hex.EncodeToString(sum[:])
		},
		"hexEncodeToString": func(s string) string {
			return hex.EncodeToString([]byte(s))
		},
		"hexDecodeToString": func(s string) string {
			b, err := hex.DecodeString(strings.TrimSpace(s))
			if err != nil {
				return ""
			}
			return string(b)
		},
		"encodeURI": func(s string) string { return url.QueryEscape(s) },
		"decodeURI": func(s string) string {
			if v, err := url.QueryUnescape(s); err == nil {
				return v
			}
			return s
		},
		"aesEncodeToString": func(s, key string, iv ...string) string {
			out, err := aesEncrypt(s, key, firstOrEmpty(iv))
			if err != nil {
				return ""
			}
			return out
		},
		"aesDecodeToString": func(s, key string, iv ...string) string {
			out, err := aesDecrypt(s, key, firstOrEmpty(iv))
			if err != nil {
				return ""
			}
			return out
		},
		"randomUUID": func() string {
			b := make([]byte, 16)
			if _, err := rand.Read(b); err != nil {
				return ""
			}
			b[6] = (b[6] & 0x0f) | 0x40
			b[8] = (b[8] & 0x3f) | 0x80
			return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
		},
		"timeFormat": func(pattern ...string) string {
			layout := javaTimeLayout("yyyy-MM-dd HH:mm:ss")
			if len(pattern) > 0 && pattern[0] != "" {
				layout = javaTimeLayout(pattern[0])
			}
			return time.Now().Format(layout)
		},
	}
}

func firstOrEmpty(list []string) string {
	if len(list) > 0 {
		return list[0]
	}
	return ""
}

// aesEncrypt AES-CBC + PKCS7，输出 base64（key/iv 不足 16 字节时补零）
func aesEncrypt(plain, key, iv string) (string, error) {
	block, err := aes.NewCipher(normalizeKey(key))
	if err != nil {
		return "", err
	}
	data := pkcs7Pad([]byte(plain), aes.BlockSize)
	out := make([]byte, len(data))
	cipher.NewCBCEncrypter(block, normalizeKey(iv)).CryptBlocks(out, data)
	return base64.StdEncoding.EncodeToString(out), nil
}

func aesDecrypt(encoded, key, iv string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return "", err
	}
	if len(raw) == 0 || len(raw)%aes.BlockSize != 0 {
		return "", fmt.Errorf("invalid cipher text length %d", len(raw))
	}
	block, err := aes.NewCipher(normalizeKey(key))
	if err != nil {
		return "", err
	}
	out := make([]byte, len(raw))
	cipher.NewCBCDecrypter(block, normalizeKey(iv)).CryptBlocks(out, raw)
	out = pkcs7Unpad(out)
	return string(out), nil
}

func normalizeKey(key string) []byte {
	b := []byte(key)
	if len(b) == 0 {
		return make([]byte, 16)
	}
	switch {
	case len(b) >= 32:
		return b[:32]
	case len(b) >= 24:
		return b[:24]
	case len(b) >= 16:
		return b[:16]
	}
	out := make([]byte, 16)
	copy(out, b)
	return out
}

func pkcs7Pad(data []byte, size int) []byte {
	n := size - len(data)%size
	return append(append([]byte{}, data...), []byte(strings.Repeat(string(rune(n)), n))...)
}

func pkcs7Unpad(data []byte) []byte {
	if len(data) == 0 {
		return data
	}
	n := int(data[len(data)-1])
	if n <= 0 || n > len(data) || n > 32 {
		return data
	}
	return data[:len(data)-n]
}

// javaTimeLayout 把 Java SimpleDateFormat 里常见的模式转成 Go layout
func javaTimeLayout(pattern string) string {
	replacer := strings.NewReplacer(
		"yyyy", "2006",
		"yy", "06",
		"MM", "01",
		"dd", "02",
		"HH", "15",
		"mm", "04",
		"ss", "05",
		"SSS", "000",
		"'T'", "T",
	)
	return replacer.Replace(pattern)
}
