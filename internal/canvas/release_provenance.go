package canvas

import "github.com/agent-primitives/canvas/internal/contract"

func suiteEvaluation(suite *contract.SuiteIdentity) string {
	if suite != nil && suite.Release != nil {
		return suite.Release.Evaluation.Status
	}
	return notEvaluated
}

// Called under c.mu. A saved UI flag is not an evaluation authority: derive the
// release panel from checked immutable bytes matching the recorded archive digest.
func (c *Canvas) recordedSuite(a *attempt) *contract.SuiteIdentity {
	digest := a.BundleDigest
	if digest == "" {
		if b, ok := c.bundles[a.Agent]; ok {
			digest = b.Digest
		}
	}
	// Keep the archive alias check effective on cache hits as well as first reads.
	key := a.Agent + ":" + digest
	if suite, ok := c.definition.verifiedSuites[key]; ok {
		return suite.Clone()
	}
	if c.definition.archives != nil && digest != "" {
		b, err := c.definition.archives.Load(digest)
		if err == nil && b.ID == a.Agent && b.Suite != nil {
			c.definition.verifiedSuites[key] = b.Suite.Clone()
			return b.Suite.Clone()
		}
	}
	if b, ok := c.bundles[a.Agent]; ok && b.Suite != nil && b.Suite.Release != nil {
		return nil
	}
	legacy := a.Suite.Clone()
	if legacy != nil {
		legacy.Release = nil
	}
	return legacy
}
