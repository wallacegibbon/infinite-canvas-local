package resource

import "fmt"

// InstallPolicy says whether this gateway may download models at all.
//
// An install writes files into ComfyUI's model folders, so it only works when the gateway
// runs on the ComfyUI machine or sees those folders through a mount. That is a property of
// the deployment and cannot be discovered reliably from inside the process: a mount at a
// different path looks exactly like a remote machine, and two hosts that both install to
// /opt/ComfyUI look exactly like one. So it is stated in the environment, never guessed:
//
//	COMFY_MODELS_SHARED=1  installs allowed, nothing is inspected
//	anything else/unset    installs refused, and the panel says why
//
// Fail-closed on purpose. The failure this prevents is not an error the user can react to:
// as root, writing into a path that happens to exist on this host succeeds, several
// gigabytes land on the wrong disk, and ComfyUI keeps reporting the model as missing with
// nothing in the logs. Refusing until the operator says "these folders are here" costs one
// line in .env, and .env.example ships it filled in.
type InstallPolicy struct {
	// Allowed is true only for COMFY_MODELS_SHARED=1.
	Allowed bool `json:"allowed"`
	// ComfyHost is COMFY_URL's host:port: the machine the gateway has to move to.
	ComfyHost string `json:"comfyHost"`
}

// InstallPolicy reports whether model installs are allowed on this host.
func (m *Manager) InstallPolicy() InstallPolicy {
	return InstallPolicy{Allowed: m.cfg.ComfyModelsShared, ComfyHost: hostOf(m.cfg.ComfyURL)}
}

// installRefusedMessage explains the refusal in terms the user can act on. Both fixes are
// named because the reader knows which one applies and the process does not.
func installRefusedMessage(policy InstallPolicy) string {
	return fmt.Sprintf(
		"model installs are off: COMFY_MODELS_SHARED is not 1, so the gateway has not been told it can reach "+
			"ComfyUI's model folders. Installs in this state would only write to this host's disk while ComfyUI "+
			"kept reporting the model as missing. If the gateway runs on the ComfyUI machine (%s) or its model "+
			"folders are mounted here, set COMFY_MODELS_SHARED=1; otherwise run the gateway there instead.",
		defaultString(policy.ComfyHost, "COMFY_URL"))
}
