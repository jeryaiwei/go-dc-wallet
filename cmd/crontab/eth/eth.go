// 定时处理检测任务
package main

import (
	"go-dc-wallet/app"
	"go-dc-wallet/heth"
	"go-dc-wallet/xenv"

	"github.com/moremorefun/mcommon"
	"github.com/robfig/cron/v3"
)

func main() {
	xenv.EnvCreate()
	defer xenv.EnvDestroy()

	c := cron.New(
		cron.WithSeconds(),
		cron.WithChain(
			cron.Recover(cron.DefaultLogger),
		),
	)
	var err error
	// --- common --
	// 检测 通知发送
	_, err = c.AddFunc("@every 1m", app.CheckDoNotify)
	if err != nil {
		mcommon.Log.Errorf("cron add func error: %#v", err)
	}

	// --- eth ---
	if xenv.Cfg.EthEnable {
		// 检测 eth 生成地址
		_, err = c.AddFunc("@every 1m", heth.CheckAddressFree)
		if err != nil {
			mcommon.Log.Errorf("cron add func error: %#v", err)
		}
		// 检测 eth 冲币
		_, err = c.AddFunc("@every 5s", heth.CheckBlockSeek)
		if err != nil {
			mcommon.Log.Errorf("cron add func error: %#v", err)
		}
		// 检测 eth 零钱整理
		_, err = c.AddFunc("@every 10m", heth.CheckAddressOrg)
		if err != nil {
			mcommon.Log.Errorf("cron add func error: %#v", err)
		}
		// 检测 eth 提币
		_, err = c.AddFunc("@every 3m", heth.CheckWithdraw)
		if err != nil {
			mcommon.Log.Errorf("cron add func error: %#v", err)
		}
		// 检测 eth 发送交易
		_, err = c.AddFunc("@every 1m", heth.CheckRawTxSend)
		if err != nil {
			mcommon.Log.Errorf("cron add func error: %#v", err)
		}
		// 检测 eth 交易上链
		_, err = c.AddFunc("@every 5s", heth.CheckRawTxConfirm)
		if err != nil {
			mcommon.Log.Errorf("cron add func error: %#v", err)
		}
		// 检测 eth 通知到账
		_, err = c.AddFunc("@every 5s", heth.CheckTxNotify)
		if err != nil {
			mcommon.Log.Errorf("cron add func error: %#v", err)
		}
		// 检测 eth gas price
		_, err = c.AddFunc("@every 2m", heth.CheckGasPrice)
		if err != nil {
			mcommon.Log.Errorf("cron add func error: %#v", err)
		}

		// --- erc20 ---
		// 检测 erc20 冲币
		_, err = c.AddFunc("@every 5s", heth.CheckErc20BlockSeek)
		if err != nil {
			mcommon.Log.Errorf("cron add func error: %#v", err)
		}
		// 检测 erc20 通知到账
		_, err = c.AddFunc("@every 5s", heth.CheckErc20TxNotify)
		if err != nil {
			mcommon.Log.Errorf("cron add func error: %#v", err)
		}
		// 检测 erc20 零钱整理
		_, err = c.AddFunc("@every 10m", heth.CheckErc20TxOrg)
		if err != nil {
			mcommon.Log.Errorf("cron add func error: %#v", err)
		}
		// 检测 erc20 提币
		_, err = c.AddFunc("@every 3m", heth.CheckErc20Withdraw)
		if err != nil {
			mcommon.Log.Errorf("cron add func error: %#v", err)
		}
	}

	c.Start()
	select {}
}
