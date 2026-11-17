package config

import "testing"

func TestChanshuiNormalHubAvailableWithoutFeatureSwitch(t *testing.T) {
	for _, previousValue := range []string{"", "false", "true"} {
		t.Run("old_switch_"+previousValue, func(t *testing.T) {
			t.Setenv("CHANSHUI_ENABLED", previousValue)
			t.Setenv("TRANSITHUB_API_ONLY", "0")
			if !Load().ChanshuiEnabled {
				t.Fatal("正常调度站必须默认提供模型检测，不应要求用户再配置功能开关")
			}
		})
	}
}
