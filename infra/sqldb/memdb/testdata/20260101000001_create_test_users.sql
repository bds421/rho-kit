-- +goose Up
CREATE TABLE test_users (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    age INTEGER NOT NULL
);

-- +goose Down
DROP TABLE test_users;
