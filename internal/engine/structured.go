package engine

import (
	"fmt"
	"strings"
	"time"

	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
	"github.com/kaladinstorm84/SignalBox/internal/schema"
)

// checkFunc validates raw output, returning the normalized document text
// and any validation errors.
type checkFunc func(text string) (string, []string)

func schemaCheck(sch *schema.Schema) checkFunc {
	return func(text string) (string, []string) {
		res := sch.Check(text)
		return res.JSON, res.Errors
	}
}

// conform validates out and, when the node allows repairs, re-invokes it
// with the validation errors until the output conforms or the repair limit
// is reached. Repairs are separate from runtime retries (which still apply
// to each call) and from gate rework.
func (eng *Engine) conform(le *liveExec, run *NodeRun, node *pipeline.Node, sch *schema.Schema, out string, check checkFunc, reinvoke func(input string) (string, error)) (string, error) {
	limit := node.RepairLimit()
	for repairs := 0; ; repairs++ {
		normalized, errs := check(out)
		eng.recordSchemaAttempt(le, run, out, normalized, errs)
		if len(errs) == 0 {
			return normalized, nil
		}
		if repairs >= limit {
			summary := strings.Join(errs, "; ")
			if limit > 0 {
				return "", fmt.Errorf("output does not match schema %q after %d repair attempt(s): %s", sch.Name, limit, summary)
			}
			return "", fmt.Errorf("output does not match schema %q: %s", sch.Name, summary)
		}
		next, err := reinvoke(schema.RepairInput(runtimeInput(run), sch.Name, out, errs))
		if err != nil {
			return "", err
		}
		out = next
	}
}

// recordSchemaAttempt keeps the raw answer for invalid attempts and for
// valid ones whose text differed from the normalized document (prose or a
// fenced block around the JSON).
func (eng *Engine) recordSchemaAttempt(le *liveExec, run *NodeRun, out, normalized string, errs []string) {
	eng.mu.Lock()
	defer eng.mu.Unlock()
	attempt := SchemaAttempt{Attempt: len(run.SchemaAttempts) + 1, Valid: len(errs) == 0, Errors: errs, At: time.Now().UTC()}
	if len(errs) > 0 || strings.TrimSpace(out) != normalized {
		attempt.Output = schema.Truncate(out, 4000)
	}
	run.SchemaAttempts = append(run.SchemaAttempts, attempt)
	eng.persistLocked(le)
}

// validateInput checks an execution's initial input against the
// pipeline's input schema.
func validateInput(p *pipeline.Pipeline, input string) error {
	name := p.InputSchema()
	if name == "" {
		return nil
	}
	sch := p.SchemaFor(name)
	if sch == nil {
		return fmt.Errorf("pipeline %q: input schema %q is not loaded", p.Name, name)
	}
	doc, err := schema.Parse(input)
	if err != nil {
		return fmt.Errorf("input does not match schema %q: input must be a JSON document (%v)", name, err)
	}
	if errs := sch.Validate(doc); len(errs) > 0 {
		return fmt.Errorf("input does not match schema %q: %s", name, strings.Join(errs, "; "))
	}
	return nil
}

// structuredDoc returns the decoded document field conditions and
// templates read: an llm_gate's verdict, otherwise the run's output. It is
// nil for runs without a schema.
func structuredDoc(run *NodeRun, output string) any {
	if run == nil || run.Schema == "" {
		return nil
	}
	text := output
	if run.Type == pipeline.TypeLLMGate {
		text = run.Verdict
	}
	doc, err := schema.Parse(text)
	if err != nil {
		return nil
	}
	return doc
}

// verdictErrors checks that a schema-validated gate verdict also carries
// the decision and feedback fields the gate needs.
func verdictErrors(doc any) []string {
	obj, ok := doc.(map[string]any)
	if !ok {
		return []string{"verdict must be a JSON object"}
	}
	var errs []string
	decision, ok := obj["decision"].(string)
	if !ok {
		errs = append(errs, `verdict must include a string field "decision"`)
	} else if decision != "approve" && decision != "reject" {
		errs = append(errs, fmt.Sprintf(`decision is %q; want "approve" or "reject"`, decision))
	}
	if _, ok := obj["feedback"].(string); !ok {
		errs = append(errs, `verdict must include a string field "feedback"`)
	}
	return errs
}
