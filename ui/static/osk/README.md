# On-Screen Keyboard Layouts

`en-us-qwerty.json` is redistributed verbatim from the Apache Guacamole
project (apache/guacamole-client) under the Apache License 2.0.

Source: https://github.com/apache/guacamole-client/blob/main/guacamole/src/main/frontend/src/layouts/en-us-qwerty.json

The file is loaded by `Guacamole.OnScreenKeyboard` in the Scrier session
page (`server/scrier/template.go`) when an operator toggles the on-screen
keyboard in an RDP or VNC session.

Additional locales (de, es, fr, it, nl, ru, tr) are available upstream —
add them here and expose a selector in the template if/when needed.
