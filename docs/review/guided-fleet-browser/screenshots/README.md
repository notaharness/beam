# Browser state captures

Actual worker asset under its CSP; virtual passkeys and intercepted slot responses. No live daemon or service.

Reproduce from worker/: `node test/page-screenshots.mjs`. c = create, a = authorize, r = revoke.

44 distinct visible states, each at 1280px and 390px in light/dark (176 PNGs). Equivalent errors (for example cancelled via the button vs NotAllowedError) share a frame; protocol combinations are exercised by flows.test.mjs.

| State | Desktop light | Desktop dark | Phone light | Phone dark |
| --- | --- | --- | --- | --- |
| start | [desktop-light](start-desktop-light.png) | [desktop-dark](start-desktop-dark.png) | [phone-light](start-phone-light.png) | [phone-dark](start-phone-dark.png) |
| start-join | [desktop-light](start-join-desktop-light.png) | [desktop-dark](start-join-desktop-dark.png) | [phone-light](start-join-phone-light.png) | [phone-dark](start-join-phone-dark.png) |
| start-app | [desktop-light](start-app-desktop-light.png) | [desktop-dark](start-app-desktop-dark.png) | [phone-light](start-app-phone-light.png) | [phone-dark](start-app-phone-dark.png) |
| invalid | [desktop-light](invalid-desktop-light.png) | [desktop-dark](invalid-desktop-dark.png) | [phone-light](invalid-phone-light.png) | [phone-dark](invalid-phone-dark.png) |
| ready-c | [desktop-light](ready-c-desktop-light.png) | [desktop-dark](ready-c-desktop-dark.png) | [phone-light](ready-c-phone-light.png) | [phone-dark](ready-c-phone-dark.png) |
| ready-a | [desktop-light](ready-a-desktop-light.png) | [desktop-dark](ready-a-desktop-dark.png) | [phone-light](ready-a-phone-light.png) | [phone-dark](ready-a-phone-dark.png) |
| ready-r | [desktop-light](ready-r-desktop-light.png) | [desktop-dark](ready-r-desktop-dark.png) | [phone-light](ready-r-phone-light.png) | [phone-dark](ready-r-phone-dark.png) |
| checking-browser | [desktop-light](checking-browser-desktop-light.png) | [desktop-dark](checking-browser-desktop-dark.png) | [phone-light](checking-browser-phone-light.png) | [phone-dark](checking-browser-phone-dark.png) |
| compatibility | [desktop-light](compatibility-desktop-light.png) | [desktop-dark](compatibility-desktop-dark.png) | [phone-light](compatibility-phone-light.png) | [phone-dark](compatibility-phone-dark.png) |
| compatibility-expanded | [desktop-light](compatibility-expanded-desktop-light.png) | [desktop-dark](compatibility-expanded-desktop-dark.png) | [phone-light](compatibility-expanded-phone-light.png) | [phone-dark](compatibility-expanded-phone-dark.png) |
| blocked-request-details | [desktop-light](blocked-request-details-desktop-light.png) | [desktop-dark](blocked-request-details-desktop-dark.png) | [phone-light](blocked-request-details-phone-light.png) | [phone-dark](blocked-request-details-phone-dark.png) |
| prf-unknown | [desktop-light](prf-unknown-desktop-light.png) | [desktop-dark](prf-unknown-desktop-dark.png) | [phone-light](prf-unknown-phone-light.png) | [phone-dark](prf-unknown-phone-dark.png) |
| prf-reported-false | [desktop-light](prf-reported-false-desktop-light.png) | [desktop-dark](prf-reported-false-desktop-dark.png) | [phone-light](prf-reported-false-phone-light.png) | [phone-dark](prf-reported-false-phone-dark.png) |
| no-webauthn | [desktop-light](no-webauthn-desktop-light.png) | [desktop-dark](no-webauthn-desktop-dark.png) | [phone-light](no-webauthn-phone-light.png) | [phone-dark](no-webauthn-phone-dark.png) |
| no-encryption | [desktop-light](no-encryption-desktop-light.png) | [desktop-dark](no-encryption-desktop-dark.png) | [phone-light](no-encryption-phone-light.png) | [phone-dark](no-encryption-phone-dark.png) |
| no-webauthn-or-encryption | [desktop-light](no-webauthn-or-encryption-desktop-light.png) | [desktop-dark](no-webauthn-or-encryption-desktop-dark.png) | [phone-light](no-webauthn-or-encryption-phone-light.png) | [phone-dark](no-webauthn-or-encryption-phone-dark.png) |
| waiting-c | [desktop-light](waiting-c-desktop-light.png) | [desktop-dark](waiting-c-desktop-dark.png) | [phone-light](waiting-c-phone-light.png) | [phone-dark](waiting-c-phone-dark.png) |
| waiting-a | [desktop-light](waiting-a-desktop-light.png) | [desktop-dark](waiting-a-desktop-dark.png) | [phone-light](waiting-a-phone-light.png) | [phone-dark](waiting-a-phone-dark.png) |
| waiting-r | [desktop-light](waiting-r-desktop-light.png) | [desktop-dark](waiting-r-desktop-dark.png) | [phone-light](waiting-r-phone-light.png) | [phone-dark](waiting-r-phone-dark.png) |
| sending-c | [desktop-light](sending-c-desktop-light.png) | [desktop-dark](sending-c-desktop-dark.png) | [phone-light](sending-c-phone-light.png) | [phone-dark](sending-c-phone-dark.png) |
| sending-a | [desktop-light](sending-a-desktop-light.png) | [desktop-dark](sending-a-desktop-dark.png) | [phone-light](sending-a-phone-light.png) | [phone-dark](sending-a-phone-dark.png) |
| sending-r | [desktop-light](sending-r-desktop-light.png) | [desktop-dark](sending-r-desktop-dark.png) | [phone-light](sending-r-phone-light.png) | [phone-dark](sending-r-phone-dark.png) |
| sent-c | [desktop-light](sent-c-desktop-light.png) | [desktop-dark](sent-c-desktop-dark.png) | [phone-light](sent-c-phone-light.png) | [phone-dark](sent-c-phone-dark.png) |
| sent-a | [desktop-light](sent-a-desktop-light.png) | [desktop-dark](sent-a-desktop-dark.png) | [phone-light](sent-a-phone-light.png) | [phone-dark](sent-a-phone-dark.png) |
| sent-r | [desktop-light](sent-r-desktop-light.png) | [desktop-dark](sent-r-desktop-dark.png) | [phone-light](sent-r-phone-light.png) | [phone-dark](sent-r-phone-dark.png) |
| prf-unsupported-c | [desktop-light](prf-unsupported-c-desktop-light.png) | [desktop-dark](prf-unsupported-c-desktop-dark.png) | [phone-light](prf-unsupported-c-phone-light.png) | [phone-dark](prf-unsupported-c-phone-dark.png) |
| prf-unsupported-a | [desktop-light](prf-unsupported-a-desktop-light.png) | [desktop-dark](prf-unsupported-a-desktop-dark.png) | [phone-light](prf-unsupported-a-phone-light.png) | [phone-dark](prf-unsupported-a-phone-dark.png) |
| prf-unsupported-r | [desktop-light](prf-unsupported-r-desktop-light.png) | [desktop-dark](prf-unsupported-r-desktop-dark.png) | [phone-light](prf-unsupported-r-phone-light.png) | [phone-dark](prf-unsupported-r-phone-dark.png) |
| cancelled | [desktop-light](cancelled-desktop-light.png) | [desktop-dark](cancelled-desktop-dark.png) | [phone-light](cancelled-phone-light.png) | [phone-dark](cancelled-phone-dark.png) |
| failed | [desktop-light](failed-desktop-light.png) | [desktop-dark](failed-desktop-dark.png) | [phone-light](failed-phone-light.png) | [phone-dark](failed-phone-dark.png) |
| failed-details | [desktop-light](failed-details-desktop-light.png) | [desktop-dark](failed-details-desktop-dark.png) | [phone-light](failed-details-phone-light.png) | [phone-dark](failed-details-phone-dark.png) |
| encryption-failed | [desktop-light](encryption-failed-desktop-light.png) | [desktop-dark](encryption-failed-desktop-dark.png) | [phone-light](encryption-failed-phone-light.png) | [phone-dark](encryption-failed-phone-dark.png) |
| delivery-409 | [desktop-light](delivery-409-desktop-light.png) | [desktop-dark](delivery-409-desktop-dark.png) | [phone-light](delivery-409-phone-light.png) | [phone-dark](delivery-409-phone-dark.png) |
| delivery-429 | [desktop-light](delivery-429-desktop-light.png) | [desktop-dark](delivery-429-desktop-dark.png) | [phone-light](delivery-429-phone-light.png) | [phone-dark](delivery-429-phone-dark.png) |
| delivery-400 | [desktop-light](delivery-400-desktop-light.png) | [desktop-dark](delivery-400-desktop-dark.png) | [phone-light](delivery-400-phone-light.png) | [phone-dark](delivery-400-phone-dark.png) |
| delivery-404 | [desktop-light](delivery-404-desktop-light.png) | [desktop-dark](delivery-404-desktop-dark.png) | [phone-light](delivery-404-phone-light.png) | [phone-dark](delivery-404-phone-dark.png) |
| delivery-413 | [desktop-light](delivery-413-desktop-light.png) | [desktop-dark](delivery-413-desktop-dark.png) | [phone-light](delivery-413-phone-light.png) | [phone-dark](delivery-413-phone-dark.png) |
| delivery-500 | [desktop-light](delivery-500-desktop-light.png) | [desktop-dark](delivery-500-desktop-dark.png) | [phone-light](delivery-500-phone-light.png) | [phone-dark](delivery-500-phone-dark.png) |
| delivery-unknown | [desktop-light](delivery-unknown-desktop-light.png) | [desktop-dark](delivery-unknown-desktop-dark.png) | [phone-light](delivery-unknown-phone-light.png) | [phone-dark](delivery-unknown-phone-dark.png) |
| sent-request-details | [desktop-light](sent-request-details-desktop-light.png) | [desktop-dark](sent-request-details-desktop-dark.png) | [phone-light](sent-request-details-phone-light.png) | [phone-dark](sent-request-details-phone-dark.png) |
| sent-technical-details | [desktop-light](sent-technical-details-desktop-light.png) | [desktop-dark](sent-technical-details-desktop-dark.png) | [phone-light](sent-technical-details-phone-light.png) | [phone-dark](sent-technical-details-phone-dark.png) |
| copied | [desktop-light](copied-desktop-light.png) | [desktop-dark](copied-desktop-dark.png) | [phone-light](copied-phone-light.png) | [phone-dark](copied-phone-dark.png) |
| copy-denied | [desktop-light](copy-denied-desktop-light.png) | [desktop-dark](copy-denied-desktop-dark.png) | [phone-light](copy-denied-phone-light.png) | [phone-dark](copy-denied-phone-dark.png) |
| long-name | [desktop-light](long-name-desktop-light.png) | [desktop-dark](long-name-desktop-dark.png) | [phone-light](long-name-phone-light.png) | [phone-dark](long-name-phone-dark.png) |
