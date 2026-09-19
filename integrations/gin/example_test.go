package gin_test

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"

	typesafe "github.com/nibir1/typesafe-go"
	tsgin "github.com/nibir1/typesafe-go/integrations/gin"
)

// Compiled and type-checked, not executed: running it needs a real key.
func Example() {
	client, err := typesafe.NewClient()
	if err != nil {
		log.Fatal(err)
	}

	r := gin.Default()
	r.Use(tsgin.Middleware(client), tsgin.Correlation())

	r.POST("/triage", func(c *gin.Context) {
		var body struct {
			Ticket string `json:"ticket"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "bad request"})
			return
		}

		// c.Request.Context(), not context.Background(): that is the context
		// carrying the client, the correlation id and the client's
		// disconnection.
		resp, err := tsgin.MustFrom(c).SystemOne(c.Request.Context(), &typesafe.SystemOneRequest{
			State: body.Ticket,
			Questions: typesafe.Questions{
				"is_urgent": typesafe.Noul{Instructions: "Does this convey urgency?"},
			},
		})
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "upstream failed"})
			return
		}

		urgent, err := resp.Noul("is_urgent")
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "unexpected answer"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"urgent": urgent.Bool(0.8), "probability": urgent.Noul})
	})

	log.Fatal(r.Run(":8080"))
}
