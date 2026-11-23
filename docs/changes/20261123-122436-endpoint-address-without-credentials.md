### Security
- `PUT /api/v1/connector-endpoints/{name}` refuses an address that holds a user name or password, or any "@", with a 422. The escaped "%40" is refused the same way. An endpoint already stored with either is no longer listed by `GET /api/v1/connector-endpoints` or dialled. It is reported as a configuration failure by its name alone.
