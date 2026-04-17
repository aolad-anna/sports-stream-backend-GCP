package firebase

import (
	"context"
	"log"

	fb "firebase.google.com/go/v4"
	"firebase.google.com/go/v4/auth"
	"google.golang.org/api/option"

	"sports-stream-backend/pkg/util"
)

var app *fb.App

// InitClient initialises the Firebase Admin App once at service startup.
// creds can be either:
//   - a file path to a service-account JSON file
//   - the raw JSON content of a service-account (e.g. from FIREBASE_CREDENTIALS env var)
//   - empty string, in which case GOOGLE_APPLICATION_CREDENTIALS env var is used
func InitClient(ctx context.Context, creds string) (*fb.App, error) {
	var opts []option.ClientOption

	if creds != "" {
		if util.LooksLikeJSONCredential(creds) {
			opts = append(opts, option.WithCredentialsJSON([]byte(creds)))
		} else if util.FileExists(creds) {
			opts = append(opts, option.WithCredentialsFile(creds))
		} else {
			log.Printf(`{"service":"firebase","level":"warn","msg":"credential path not found, falling back to default credentials","path":%q}`, creds)
		}
	}

	var err error
	app, err = fb.NewApp(ctx, nil, opts...)
	if err != nil {
		return nil, err
	}
	log.Println(`{"service":"firebase","level":"info","msg":"firebase admin sdk initialised"}`)
	return app, nil
}

// GetApp returns the initialised Firebase App. Panics if InitClient was not called.
func GetApp() *fb.App {
	if app == nil {
		panic("firebase.InitClient must be called before GetApp")
	}
	return app
}

// VerifyIDToken verifies a Firebase ID token string and returns the decoded token.
// Called by every service that needs to authenticate Android requests.
func VerifyIDToken(ctx context.Context, idToken string) (*auth.Token, error) {
	client, err := GetApp().Auth(ctx)
	if err != nil {
		return nil, err
	}
	return client.VerifyIDToken(ctx, idToken)
}
