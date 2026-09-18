package echo_test

import (
	"log"
	"net/http"

	"github.com/labstack/echo/v4"
	typesafe "github.com/nibir1/typesafe-go"
	tsecho "github.com/nibir1/typesafe-go/integrations/echo"
)

// Compiled and type-checked, not executed: running it needs a real key.
func Example() {
	client, err := typesafe.NewClient()
	if err != nil {
		log.Fatal(err)
	}

	e := echo.New()
	e.Use(tsecho.Middleware(client), tsecho.Correlation())

	e.POST("/triage", func(c echo.Context) error {
		var body struct {
			Ticket string `json:"ticket"`
		}
		if err := c.Bind(&body); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "bad request")
		}

		resp, err := tsecho.MustFrom(c).SystemOne(c.Request().Context(), &typesafe.SystemOneRequest{
			State: body.Ticket,
			Questions: typesafe.Questions{
				"is_urgent": typesafe.Noul{Instructions: "Does this convey urgency?"},
			},
		})
		if err != nil {
			return echo.NewHTTPError(http.StatusBadGateway, "upstream failed")
		}

		urgent, err := resp.Noul("is_urgent")
		if err != nil {
			return echo.NewHTTPError(http.StatusBadGateway, "unexpected answer")
		}
		return c.JSON(http.StatusOK, map[string]any{
			"urgent":      urgent.Bool(0.8),
			"probability": urgent.Noul,
		})
	})

	log.Fatal(e.Start(":8080"))
}
