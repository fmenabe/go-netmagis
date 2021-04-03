package netmagis

type NetmagisError struct {
	msg string
}

func (error *NetmagisError) Error() string {
	return error.msg
}
