CREATE TABLE users (
  id UUID PRIMARY KEY,
  email VARCHAR(300) UNIQUE NOT NULL,
  password_hash TEXT NOT NULL,
  role TEXT NOT NULL CHECK (role IN ('buyer', 'seller')) 
);