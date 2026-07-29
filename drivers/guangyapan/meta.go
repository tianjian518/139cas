package guangyapan

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	driver.RootID
	AccessToken  string `json:"access_token" required:"true" type:"text" default:""`
	RefreshToken string `json:"refresh_token" required:"false" type:"text" default:""`
	ClientID     string `json:"client_id" required:"true" default:"aMe-8VSlkrbQXpUR"`
	DeviceID     string `json:"device_id" required:"true" default:""`
	PageSize     int    `json:"page_size" required:"true" type:"number" default:"100"`
	OrderBy      int    `json:"order_by" required:"true" type:"number" default:"3"`
	SortType     int    `json:"sort_type" required:"true" type:"number" default:"1"`
}

var config = driver.Config{
	Name:        "GuangYaPan",
	LocalSort:   true,
	DefaultRoot: "",
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &GuangYaPan{}
	})
}
