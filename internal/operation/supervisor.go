package operation

const supervisorFlag = "--hhc-internal-media-supervisor"

// HandleSupervisor is called explicitly by the executable entrypoint before
// normal command parsing. It is not a public CLI command or an auth boundary.
func HandleSupervisor(args []string) (bool, int) {
	if len(args) != 1 || args[0] != supervisorFlag {
		return false, 0
	}
	return true, superviseMedia()
}

type supervisorRequest struct {
	Binary string
	Args   []string
}
