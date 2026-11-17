### Fixed
- Adding a person with the admin role on the People page now gives them that role. Before, the server refused it with ACCESS_DENIED after it had already created the account, which was left holding no role.
