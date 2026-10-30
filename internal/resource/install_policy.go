package resource

import (
	"fmt"
	"strings"
)

// InstallPolicy says whether this gateway may write into ComfyUI's folders.
//
// An install writes files into ComfyUI's model and custom_nodes folders, so it only works when
// the gateway runs on the ComfyUI machine or sees those folders through a mount. That is a property of
// the deployment and cannot be discovered reliably from inside the process: a mount at a
// different path looks exactly like a remote machine, and two hosts that both install to
// /opt/ComfyUI look exactly like one. So it is stated in the environment, never guessed:
//
//	INSTALL_INTO_COMFY=1  installs allowed, nothing is inspected
//	anything else/unset    installs refused, and the panel says why
//
// Fail-closed on purpose. The failure this prevents is not an error the user can react to:
// as root, writing into a path that happens to exist on this host succeeds, several
// gigabytes land on the wrong disk, and ComfyUI keeps reporting the model as missing with
// nothing in the logs. Refusing until the operator says "these folders are here" costs one
// line in .env, and .env.example ships it filled in.
type InstallPolicy struct {
	// Allowed is true only for INSTALL_INTO_COMFY=1.
	Allowed bool `json:"allowed"`
	// ComfyHost is COMFY_URL's host:port: the machine the gateway has to move to.
	ComfyHost string `json:"comfyHost"`
}

// InstallPolicy reports whether model installs are allowed on this host.
func (m *Manager) InstallPolicy() InstallPolicy {
	return InstallPolicy{Allowed: m.cfg.InstallIntoComfy, ComfyHost: hostOf(m.cfg.ComfyURL)}
}

// installRootUnknownMessage explains that the ComfyUI directory could not be determined.
//
// The capability, not a version number, is what decides: a build without
// GET /internal/folder_paths, or a proxy that hides /internal/*, both end up here, and the
// version ComfyUI reported goes into the message so a bug report needs no follow-up question.
func (m *Manager) installRootUnknownMessage() string {
	version := ""
	if m.setup != nil {
		version = m.setup.Status().ComfyVersion
	}
	detected := "ComfyUI"
	if version != "" {
		detected = "ComfyUI " + version
	}
	where := ""
	if url := strings.TrimSpace(m.cfg.ComfyURL); url != "" {
		where = " at " + url
	}
	return fmt.Sprintf(
		"cannot tell where %s is installed: %s did not report a custom_nodes folder "+
			"(GET /internal/folder_paths), which is the only thing that says where an install may write. "+
			"Upgrade ComfyUI%s so it serves that endpoint, or stop hiding /internal/* behind a proxy.",
		detected, detected, where)
}

// installRefusedMessage explains the refusal in terms the user can act on. Both fixes are
// named because the reader knows which one applies and the process does not.
func installRefusedMessage(policy InstallPolicy) string {
	return fmt.Sprintf(
		"model installs are off: INSTALL_INTO_COMFY is not 1, so the gateway has not been told it may write "+
			"into ComfyUI's folders. Installs in this state would only fill this host's disk while ComfyUI kept "+
			"reporting the model as missing. If the gateway runs on the ComfyUI machine (%s) or those folders "+
			"are mounted here, set INSTALL_INTO_COMFY=1; otherwise run the gateway there instead.",
		defaultString(policy.ComfyHost, "COMFY_URL"))
}
