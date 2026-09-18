package fiber_test

import (
	"log"

	"github.com/gofiber/fiber/v3"
	typesafe "github.com/nibir1/typesafe-go"
	tsfiber "github.com/nibir1/typesafe-go/integrations/fiber"
)

// Compiled and type-checked, not executed: running it needs a real key.
func Example() {
	client, err := typesafe.NewClient()
	if err != nil {
		log.Fatal(err)
	}

	app := fiber.New()
	app.Use(tsfiber.Middleware(client), tsfiber.Correlation())

	app.Post("/triage", func(c fiber.Ctx) error {
		var body struct {
			Ticket string `json:"ticket"`
		}
		if err := c.Bind().Body(&body); err != nil {
			return fiber.ErrBadRequest
		}

		// c.Context() is the context carrying the client and the correlation
		// id, and the one cancelled when the caller hangs up.
		resp, err := tsfiber.MustFrom(c).SystemOne(c.Context(), &typesafe.SystemOneRequest{
			State: body.Ticket,
			Questions: typesafe.Questions{
				"is_urgent": typesafe.Noul{Instructions: "Does this convey urgency?"},
			},
		})
		if err != nil {
			return fiber.ErrBadGateway
		}

		urgent, err := resp.Noul("is_urgent")
		if err != nil {
			return fiber.ErrBadGateway
		}
		return c.JSON(fiber.Map{"urgent": urgent.Bool(0.8), "probability": urgent.Noul})
	})

	log.Fatal(app.Listen(":8080"))
}
