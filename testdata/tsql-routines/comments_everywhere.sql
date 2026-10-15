-- Header comment before the definition.
/* A block comment
   over several lines. */
CREATE PROCEDURE /* inline */ acme.commented -- after the name
    @a INT /* after a type */ = 1 -- after a default
AS -- after AS
BEGIN -- after BEGIN
    /* before a statement */ SELECT @a -- after a statement
    FROM acme.orders; /* after a semicolon */
    -- before END
END -- after END
-- trailing comment
