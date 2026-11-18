package questions

import (
	"bytes"
	"math/rand"
	"reflect"
	"testing"

	"github.com/kiss-kedaya/chanshui/internal/iq"
)

func TestOriginalPaperUsesOriginalSharedRandomSourceAndAllFields(t *testing.T) {
	for _, seed := range []int64{1, 73, 998877} {
		original := iq.Paper(rand.New(rand.NewSource(seed)))
		for _, item := range original {
			definition, ok := Preset(item.ID)
			if !ok {
				t.Fatal(item.ID)
			}
			got, err := Generate(definition, seed)
			if err != nil {
				t.Fatal(err)
			}
			if got.Prompt != item.Prompt || got.Expected != item.Answer || !reflect.DeepEqual(got.Tags, item.Tags) || !bytes.Equal(got.Image, item.ImagePNG) || got.Definition.MaxScore != item.MaxScore {
				t.Fatalf("original generator fields changed: seed=%d id=%s", seed, item.ID)
			}
		}
	}
}
