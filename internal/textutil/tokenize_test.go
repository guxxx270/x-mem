package textutil

import (
	"reflect"
	"strings"
	"testing"
)

func TestTermsChineseAndEnglish(t *testing.T) {
	got := Terms("我们哪天发版？Pushing the tests")
	want := []string{"我们", "们哪", "哪天", "天发", "发版", "push", "test"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Terms = %v, want %v", got, want)
	}
}

func TestFullWidthFolds(t *testing.T) {
	if got := Normalize("ＡＰＩ　Ｖ２"); got != "api v2" {
		t.Fatalf("Normalize = %q", got)
	}
}

func TestStem(t *testing.T) {
	cases := map[string]string{
		"pushing": "push", "pushes": "push", "push": "push",
		"tests": "test", "testing": "test",
		"release": "releas", "released": "releas", "releases": "releas",
		"running": "run", "stopped": "stop", "callbacks": "callback",
		"policies": "policy", "status": "status",
	}
	for in, want := range cases {
		if got := Stem(in); got != want {
			t.Errorf("Stem(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUnitsDropFunctionWords(t *testing.T) {
	var got []string
	for _, u := range Units("接口超时设多少？我们的 CI 用什么") {
		got = append(got, u.Text)
	}
	want := []string{"接", "口", "超", "时", "设", "ci", "用"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("Units = %v, want %v", got, want)
	}
}

func TestUnitPairs(t *testing.T) {
	us := Units("哪天发版")
	if len(us) != 3 || us[1].Text != "发" || !reflect.DeepEqual(us[1].Pairs, []string{"天发", "发版"}) {
		t.Fatalf("unexpected units %+v", us)
	}
	iso := Units("用 Go")
	if !iso[0].Isolated {
		t.Fatalf("a lone character should be isolated: %+v", iso)
	}
}

func TestContainsRespectsWordBoundaries(t *testing.T) {
	if !Contains("should i run tests before push", "run tests") {
		t.Fatal("phrase should match")
	}
	if Contains("rerun tests", "run tests") {
		t.Fatal("must not match inside a word")
	}
	if !Contains("下午我要把剪藏原件挪走", "剪藏原件") {
		t.Fatal("chinese phrase should match anywhere")
	}
}

func TestEstimateTokens(t *testing.T) {
	if n := EstimateTokens("发版改到周二 release day"); n < 7 || n > 10 {
		t.Fatalf("EstimateTokens = %d", n)
	}
}
