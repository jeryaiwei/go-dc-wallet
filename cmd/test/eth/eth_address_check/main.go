// 检测eth剩余可用地址是否满足需求，
// 如果不足则创建地址
package main

import (
	"go-dc-wallet/heth"
	"go-dc-wallet/xenv"
)

func main() {
	xenv.EnvCreate()
	defer xenv.EnvDestroy()

	heth.CheckAddressFree()
}
