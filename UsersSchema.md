# model.model.UsersSchema

```
model.model.Users
|——————id  	BIGINT		PrimaryKey
|------username	 VARCHAR(50)   NOTNULL
|------email 	VARCHAR(255)	NOTNULL
|------password_hash  VARCHAR(255)	NOTNULL
|------created_at	DATETIME(3) NOT NULL
|------updated_at DATETIME(3) NOT NULL 

UNIQUE KEY uk_model.model.Users_username (username),
UNIQUE KEY uk_model.model.Users_email (email)

```

