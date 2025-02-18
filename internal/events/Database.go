package events

import (
	"context"
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"time"

	"GopherMart/internal/errorsGM"
)

var CreateTableOperations = `CREATE TABLE OperationsGopherMart(
order_number     	varchar(32),   
login          		varchar(64),
uploaded_at       	varchar(32),
status				varchar(32),
operation 			varchar(32),
points      		integer,
)`

var CreateTableUsers = `CREATE TABLE UsersGopherMart(
login				varchar(32),
password          	varchar(64),
current_points    	integer,
withdrawn_points  	integer,
cookie 				varchar(32),
)`

type Operation struct {
	Order_number string  `json:"order"`
	Status       string  `json:"status"`
	Points       float64 `json:"accrual"`
	Uploaded_at  string  `json:"uploaded_at"`
}

const (
	accrual  = "accrual"
	withdraw = "withdraw"

	newOrder   = "NEW"
	processing = "PROCESSING"
	registered = "REGISTERED"
	invalid    = "INVALID"
)

type DBI interface {
	Connect(connStr string) (err error)
	CreateTable() error
	Ping(ctx context.Context) error
	Close() error

	RegisterUser(login string, pass string) (tokenJWT string, err error)
	LoginUser(login string, pass string) (tokenJWT string, err error)

	WriteOrderAccrual(order string, user string) (err error)
	ReadAllOrderAccrualUser(user string) (ops []Operation, err error)
	ReadUserPoints(user string) (u UserPoints, err error)
	WithdrawnUserPoints(user string, order string, sum float64) (err error)
	WriteOrderWithdrawn(order string, user string, point float64) (err error)
	ReadAllOrderWithdrawnUser(user string) (ops []Operation, err error)

	ReadAllOrderAccrualNoComplite() (orders []orderstruct, err error)
	UpdateOrderAccrual(login string, orderAccrual requestAccrual) (err error)
}

type Database struct {
	connection *sql.DB
}

func InitDB() (*Database, error) {
	return &Database{}, nil
}

func (db *Database) Connect(connStr string) (err error) {
	db.connection, err = sql.Open("pgx", connStr)
	if err != nil {
		return err
	}
	if err = db.CreateTable(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err = db.Ping(ctx); err != nil {
		return err
	}
	return nil
}

func (db *Database) CreateTable() error {
	if _, err := db.connection.Exec("Drop TABLE OperationsGopherMart"); err != nil {
		return err
	}
	if _, err := db.connection.Exec("Drop TABLE UsersGopherMart"); err != nil {
		return err
	}
	if _, err := db.connection.Exec(CreateTableOperations); err != nil {
		return err
	}
	if _, err := db.connection.Exec("CREATE UNIQUE INDEX order_index ON OperationsGopherMart (order_number)"); err != nil {
		return err
	}
	if _, err := db.connection.Exec(CreateTableUsers); err != nil {
		return err
	}
	_, err := db.connection.Exec("CREATE UNIQUE INDEX login_index ON UsersGopherMart (Login,users)")
	return err
}

func (db *Database) Ping(ctx context.Context) error {
	if err := db.connection.PingContext(ctx); err != nil {
		return err
	}
	return nil
}

func (db *Database) Close() error {
	return db.connection.Close()
}

// добавление заказа для начисления
func (db *Database) WriteOrderAccrual(order string, user string) (err error) {
	timeNow := time.Now().Format(time.RFC3339)

	var loginOrder string
	row := db.connection.QueryRow("select login from OperationsGopherMart where order_number = $1",
		order)
	if err = row.Scan(&loginOrder); err != nil {
		return err
	}
	if loginOrder != "" {
		if loginOrder == user {
			return errorsGM.ErrLoadedEarlierThisUser // надо что то вернуть
		}
		return errorsGM.ErrLoadedEarlierAnotherUser
	}
	_, err = db.connection.Exec("insert into OperationsGopherMart (order_number, Login, operation, uploaded_at) values ($1,$2,$3,$4)", order, user, accrual, timeNow)
	if err != nil {
		return err
	}
	return nil
}

// вывод всех заказов пользователя
func (db *Database) ReadAllOrderAccrualUser(user string) (ops []Operation, err error) {
	var op Operation
	rows, err := db.connection.Query("select order_number, status, uploaded_at, points  from OperationsGopherMart where login = $1 and operation != $2", user, accrual)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		err := rows.Scan(&op.Order_number, &op.Status, &op.Uploaded_at, &op.Points)
		if err != nil {
			return nil, err
		}
		op.Points = op.Points / 100
		ops = append(ops, op)
	}
	return ops, nil
}

type UserPoints struct {
	CurrentPoints   float64 `json:"current"`
	WithdrawnPoints float64 `json:"withdrawn"`
}

// информация о потраченных и остатках баллов
func (db *Database) ReadUserPoints(user string) (up UserPoints, err error) {
	row := db.connection.QueryRow("select current_points, withdrawn_points from UsersGopherMart where login = $1",
		user)
	if err = row.Scan(&up.CurrentPoints, &up.WithdrawnPoints); err != nil {
		return UserPoints{}, err
	}
	up.WithdrawnPoints = up.WithdrawnPoints / 100
	up.CurrentPoints = up.CurrentPoints / 100
	return up, nil
}

// списание
func (db *Database) WithdrawnUserPoints(user string, order string, sum float64) (err error) {
	var u UserPoints

	u, err = db.ReadUserPoints(user)
	if err != nil {
		return err
	}
	if u.CurrentPoints < sum {
		return errorsGM.ErrDontHavePoints
	}

	err = db.WriteOrderWithdrawn(user, order, sum)
	if err != nil {
		return err
	}

	_, err = db.connection.Exec("UPDATE UsersGopherMart SET current_points = current_points - $1 and withdrawn_points = withdrawn_points + $1 WHERE login=$2",
		sum*100, user)
	if err != nil {
		return err
	}

	return nil
}

func (db *Database) WriteOrderWithdrawn(user string, order string, point float64) (err error) {
	timeNow := time.Now().Format(time.RFC3339)
	_, err = db.connection.Exec("insert into OperationsGopherMart (order_number, users, operation, points, uploaded_at) values ($1,$2,$3,$4,$5)",
		order, user, withdraw, point*100, timeNow)
	if err != nil {
		return err
	}
	return nil
}

func (db *Database) ReadAllOrderWithdrawnUser(user string) (ops []Operation, err error) {
	var op Operation
	rows, err := db.connection.Query("select order_number, status, uploaded_at, points from OperationsGopherMart where login = $1 and operation != $2", user, withdraw)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		err := rows.Scan(&op.Order_number, &op.Status, &op.Uploaded_at, &op.Points)
		if err != nil {
			return nil, err
		}
		op.Points = op.Points / 100
		ops = append(ops, op)
	}
	return ops, nil
}

// регистрация
func (db *Database) RegisterUser(login string, pass string) (tokenJWT string, err error) {
	h := md5.New()
	h.Write([]byte(pass))
	passHex := hex.EncodeToString(h.Sum(nil))

	_, err = db.connection.Exec("insert into UsersGopherMart (login, password, current_points, withdrawn_points ) values ($1,$2,$3,$3)", login, passHex, 0)
	if err != nil {
		return "", err
	}

	tokenJWT, err = EncodeJWT(login)
	if err != nil {
		return "", err
	}
	return tokenJWT, nil
}

// авторизация
func (db *Database) LoginUser(login string, pass string) (tokenJWT string, err error) {
	h := md5.New()
	h.Write([]byte(pass))
	pass = hex.EncodeToString(h.Sum(nil))
	var dbPass string

	row := db.connection.QueryRow("select password from UsersGopherMart where login = $1",
		login)
	if err = row.Scan(&dbPass); err != nil {
		return "", err
	}
	if dbPass != pass {
		return "", nil
	}
	tokenJWT, err = EncodeJWT(login)
	if err != nil {
		return "", err
	}
	return tokenJWT, nil
}

type orderstruct struct {
	Order string
	Login string
}

func (db *Database) ReadAllOrderAccrualNoComplite() (orders []orderstruct, err error) {
	var order orderstruct
	rows, err := db.connection.Query("select order_number,login from OperationsGopherMart where status = $1 or $2",
		newOrder, processing)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		err := rows.Scan(&order.Order, &order.Login)
		if err != nil {
			return nil, err
		}
		orders = append(orders, order)
	}
	return orders, nil
}

func (db *Database) UpdateOrderAccrual(login string, orderAccrual requestAccrual) (err error) {

	_, err = db.connection.Exec("UPDATE OperationsGopherMart SET status = $1,point = $2 WHERE Order=$3",
		orderAccrual.Status, orderAccrual.Accrual, orderAccrual.Order)
	if err != nil {
		return err
	}
	//зачислить балы пользователю
	if orderAccrual.Status == registered {
		_, err = db.connection.Exec("UPDATE UsersGopherMart SET current_points = current_points + $1 WHERE Login=$2",
			orderAccrual.Accrual, login)
		if err != nil {
			return err
		}
	}
	return nil
}
