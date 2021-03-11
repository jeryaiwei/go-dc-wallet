package main

import (
	"context"
	"go-dc-wallet/eosclient"
	"go-dc-wallet/model"
	"go-dc-wallet/xenv"

	"github.com/moremorefun/mcommon"
)

func main() {
	xenv.EnvCreate()
	defer xenv.EnvDestroy()

	rpcChainInfo, err := eosclient.RPCChainGetInfo()
	if err != nil {
		mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
		return
	}
	appStatusIntRows := []*model.DBTAppStatusInt{
		{
			// eos blocknum
			K: "eos_seek_num",
			V: rpcChainInfo.LastIrreversibleBlockNum,
		},
	}
	_, err = model.SQLCreateManyTAppStatusInt(
		context.Background(),
		xenv.DbCon,
		appStatusIntRows,
		true,
	)
	if err != nil {
		mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
		return
	}

}
