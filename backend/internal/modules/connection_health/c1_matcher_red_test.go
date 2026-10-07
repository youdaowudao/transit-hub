package connection_health

import (
	"encoding/json"
	"os"
	"testing"
)

func TestC1QuestionAnswerKeywordFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/c1_keyword_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		ID        string                 `json:"id"`
		Answer    string                 `json:"answer"`
		Keywords  []string               `json:"keywords"`
		Judgment  QuestionAnswerJudgment `json:"judgment"`
		Judgeable bool                   `json:"judgeable"`
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 19 {
		t.Fatal("keyword contract cases missing")
	}
	for _, tc := range cases {
		t.Run(tc.ID, func(t *testing.T) {
			judgment, judgeable := judgeQuestionAnswer(tc.Answer, tc.Keywords)
			if judgment != tc.Judgment || judgeable != tc.Judgeable {
				t.Fatalf("judgment=%s judgeable=%v; want %s/%v", judgment, judgeable, tc.Judgment, tc.Judgeable)
			}
		})
	}
}
