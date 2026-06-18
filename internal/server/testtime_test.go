package server_test

import "time"

func timeNowMillis() int64 { return time.Now().UnixMilli() }
