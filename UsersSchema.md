# UsersSchema

```
users
|——————id  	BIGINT		PrimaryKey
|------username	 VARCHAR(50)   NOTNULL
|------email 	VARCHAR(255)	NOTNULL
|------password_hash  VARCHAR(255)	NOTNULL
|------created_at	DATETIME(3) NOT NULL
|------updated_at DATETIME(3) NOT NULL 

UNIQUE KEY uk_users_username (username),
UNIQUE KEY uk_users_email (email)

```

