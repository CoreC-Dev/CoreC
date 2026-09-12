// Package all imports all transport packages to register their factories.
package all

import (
	_ "github.com/CoreC-Dev/CoreC/transport/httppush"
	_ "github.com/CoreC-Dev/CoreC/transport/mqtt"
)
