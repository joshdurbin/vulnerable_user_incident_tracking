package dedupe

import (
	"strings"
	"testing"
)

const story = `A 34-year-old man was struck and killed by a pickup truck while crossing Main Street at Fifth Avenue on
Tuesday evening, police said. The driver stayed at the scene and is cooperating with investigators. Officers
responded to the intersection at about 7:15 p.m. and found the pedestrian lying in the roadway. He was taken to
Mercy General Hospital, where he was pronounced dead. The department's crash reconstruction unit closed the
intersection for several hours while it measured skid marks and collected video from nearby businesses. No charges
had been filed as of Wednesday morning. Neighbors said the crossing has no signal and that drivers regularly
speed through the block. A council member said the city had studied the intersection last year and put a
signal on its list of safety projects, but funding had not been approved. Police asked anyone with video of the
crash to contact the traffic division. The victim's name has not been released pending notification of family.`

// fullStory is an article-length text (about 500 words), the size SimHash is meant for.
var fullStory = story + " " + strings.ReplaceAll(story, "Tuesday", "that night") + " " + strings.ReplaceAll(story, "Main", "Elm")

func TestFingerprintIgnoresFormatting(t *testing.T) {
	a, ok := Fingerprint(story)
	if !ok {
		t.Fatal("story should be long enough to fingerprint")
	}
	b, _ := Fingerprint(strings.ToUpper(strings.ReplaceAll(story, "\n", "  \n\n ")) + "!!!")
	if a != b {
		t.Errorf("case, spacing and punctuation changed the fingerprint: distance %d", Distance(a, b))
	}
}

func TestFingerprintToleratesSmallEdits(t *testing.T) {
	a, _ := Fingerprint(fullStory)
	edits := map[string]string{
		"prefix":   "UPDATE: " + fullStory,
		"suffix":   fullStory + " This story was originally published by the Metro Courier.",
		"one word": strings.Replace(fullStory, "Tuesday", "Monday", 1),
		"sentence": strings.Replace(fullStory, "The victim's name has not been released pending notification of family.", "The victim was identified as a local teacher.", 1),
	}
	for name, text := range edits {
		b, ok := Fingerprint(text)
		if !ok {
			t.Fatalf("%s: should fingerprint", name)
		}
		if d := Distance(a, b); d > MaxDistance {
			t.Errorf("%s: a light rewrite is %d bits away, want <= %d", name, d, MaxDistance)
		}
	}
}

func TestFingerprintSeparatesDifferentStories(t *testing.T) {
	other := `A cyclist was seriously injured Saturday morning when a delivery van turned left across the bike lane on
Riverside Drive near the municipal pool. Witnesses told officers the van's driver did not see the rider, who was
thrown over the hood and landed on the curb. Paramedics treated the 52-year-old at the scene for a broken pelvis and
head injuries and flew the cyclist to a trauma centre. The van driver was cited for failing to yield. Cycling advocates
said the lane is a painted stripe with no barrier and has been the site of three other collisions since spring.
They are asking the transportation department to install flexible posts before school resumes. The department said
it would review the request at its next meeting and noted that a protected lane is planned for the corridor
as part of a repaving project scheduled for late next year, depending on the outcome of the budget vote in the autumn.`
	a, _ := Fingerprint(fullStory)
	b, ok := Fingerprint(other)
	if !ok {
		t.Fatal("other story should fingerprint")
	}
	if d := Distance(a, b); d <= MaxDistance {
		t.Errorf("different stories are only %d bits apart", d)
	}
}

func TestFingerprintSkipsShortText(t *testing.T) {
	if _, ok := Fingerprint("A pedestrian was hit on Main Street. Police are investigating."); ok {
		t.Error("a two-sentence brief should not be fingerprinted")
	}
}

func TestCanonicalKey(t *testing.T) {
	cases := []struct{ name, canonical, final, want string }{
		{"canonical wins", "https://news.example/story-1?utm_source=x", "https://news.example/amp/story-1", "https://news.example/story-1"},
		{"falls back to final URL", "", "http://news.example/story-1/#comments", "https://news.example/story-1"},
		{"bad canonical falls back", "not a url", "https://news.example/story-1", "https://news.example/story-1"},
		{"none", "", "", ""},
	}
	for _, c := range cases {
		if got := CanonicalKey(c.canonical, c.final); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
