package authoring

import "strings"

// These pins identify the existing released fixtures, not evaluated user agents.
const writerPin = "sha256:b9513f8f72b41ea36fde2179da726f62d1d28412402621023cfc0f23cadb3544"
const reviewerPin = "sha256:6ea51f953d1d51be7316fb155cbce783e7010afb43169e479f44e0cce2783ccc"

const templateHeader = `apiVersion: canvas.local/v1alpha2
kind: Canvas
metadata:
  name: "NAME"
  title: "NAME — deterministic fixture"
spec:
  documents:
    draft:
      path: draft.json
      schema: schemas/document.json
      title: Your draft
`
const writerAgent = `    writer:
      name: Writer — deterministic fixture
      suite:
        ref: WRITER_PIN
        layout: layouts/writer
        member: writer
        platform: linux/amd64
      read: [draft]
      propose: [draft]
`
const reviewerAgent = `    reviewer:
      name: Reviewer — deterministic fixture
      suite:
        ref: REVIEWER_PIN
        layout: layouts/reviewer
        member: reviewer
        platform: linux/amd64
      read: [draft, review]
      propose: [review]
`

func templateFiles(template, name string) map[string][]byte {
	definition := templateHeader
	if template == "writer-review" {
		definition += `    review:
      path: review.json
      schema: schemas/document.json
      title: Independent review
`
	}
	definition += "  agents:\n" + writerAgent
	if template == "writer-review" {
		definition += reviewerAgent
	}
	definition += "  seed:\n    draft.json: seeds/draft.json\n"
	if template == "writer-review" {
		definition += "    review.json: seeds/review.json\n"
	}
	definition += `  triggers:
    - name: draft
      type: manual
      agent: writer
`
	if template == "writer-review" {
		definition += `    - name: review
      type: manual
      agent: reviewer
`
	}
	definition = strings.NewReplacer("NAME", name, "WRITER_PIN", writerPin, "REVIEWER_PIN", reviewerPin).Replace(definition)
	files := map[string][]byte{
		"canvas.yaml":           []byte(definition),
		"schemas/document.json": []byte(`{"type":"object","required":["text"],"additionalProperties":false,"properties":{"text":{"type":"string","minLength":1,"maxLength":4096}}}` + "\n"),
		"seeds/draft.json":      []byte("{\"text\":\"Describe the document you want to review.\"}\n"),
	}
	if template == "writer-review" {
		files["seeds/review.json"] = []byte("{\"text\":\"No review yet.\"}\n")
	}
	return files
}
