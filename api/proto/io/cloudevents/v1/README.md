<!--
SPDX-License-Identifier: Apache-2.0
SPDX-FileCopyrightText: The Pumi Authors
-->

# CloudEvents protobuf envelope

`cloudevents.proto` is the CloudEvents protobuf format's `CloudEvent` message, the envelope of every Pumi event ([decision 0132](https://github.com/getpumi/design/blob/main/decisions/0132-protobuf-event-and-fact-schemas.md)). It is kept unchanged from the CloudEvents specification:

- Source: [`cloudevents/formats/cloudevents.proto`](https://github.com/cloudevents/spec/blob/ac063d464e8ef40c75b9aa3feea8a98da6f4bace/cloudevents/formats/cloudevents.proto) at commit `ac063d464e8ef40c75b9aa3feea8a98da6f4bace` of github.com/cloudevents/spec.
- SHA-256: `26722148a99be53c54cfb987a2220d3baf4c248a5dacac2b83793b5e4e2f65e5`.
- License: Apache-2.0, the license of the CloudEvents specification repository. That repository has no NOTICE file.

The file carries no SPDX lines, because it is kept unchanged; the SPDX check exempts it by its path. `buf lint` does not check it, and buf's managed mode sets its Go package to `getpumi.dev/pumi/api/gen/io/cloudevents/v1` in place of the `go_package` option in the file.
