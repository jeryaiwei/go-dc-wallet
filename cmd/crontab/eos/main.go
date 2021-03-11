// 定时处理检测任务
package main

import (
	"go-dc-wallet/app"
	"go-dc-wallet/heos"
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

	// --- eos ---
	if xenv.Cfg.EosEnable {
		// --- eos ---
		// 检测 eos 生成地址
		_, err = c.AddFunc("@every 1m", heos.CheckAddressFree)
		if err != nil {
			mcommon.Log.Errorf("cron add func error: %#v", err)
		}
		// 检测 eos 冲币
		_, err = c.AddFunc("@every 3s", heos.CheckBlockSeek)
		if err != nil {
			mcommon.Log.Errorf("cron add func error: %#v", err)
		}
		// 检测 eos 提币
		_, err = c.AddFunc("@every 3m", heos.CheckWithdraw)
		if err != nil {
			mcommon.Log.Errorf("cron add func error: %#v", err)
		}
		// 检测 eos 发送交易
		_, err = c.AddFunc("@every 1s", heos.CheckRawTxSend)
		if err != nil {
			mcommon.Log.Errorf("cron add func error: %#v", err)
		}
		// 检测 eos 交易上链
		_, err = c.AddFunc("@every 3s", heos.CheckRawTxConfirm)
		if err != nil {
			mcommon.Log.Errorf("cron add func error: %#v", err)
		}
		// 检测 eos 通知到账
		_, err = c.AddFunc("@every 3s", heos.CheckTxNotify)
		if err != nil {
			mcommon.Log.Errorf("cron add func error: %#v", err)
		}
	}

	c.Start()
	select {}
}
