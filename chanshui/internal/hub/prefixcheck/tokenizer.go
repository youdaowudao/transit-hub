package prefixcheck

import (
	"sync"

	tiktoken "github.com/pkoukk/tiktoken-go"
	loader "github.com/pkoukk/tiktoken-go-loader"
)

const TokenizerName = "o200k_base"

var (
	tokOnce sync.Once
	tokEnc  *tiktoken.Tiktoken
	tokErr  error
)

type CountFunc func(string) (int, error)

// Count loads the bundled vocabulary on demand and treats special markers as text.
func Count(text string) (int, error) {
	tokOnce.Do(func() {
		tiktoken.SetBpeLoader(loader.NewOfflineLoader())
		tokEnc, tokErr = tiktoken.GetEncoding(TokenizerName)
	})
	if tokErr != nil {
		return 0, tokErr
	}
	return len(tokEnc.EncodeOrdinary(text)), nil
}
