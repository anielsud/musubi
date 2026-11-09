package m365

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

// Person is one directory entry: who someone is in the organisation.
type Person struct {
	Email        string    `json:"email"`
	Name         string    `json:"name"`
	GivenName    string    `json:"givenName,omitempty"`
	Surname      string    `json:"surname,omitempty"`
	Nickname     string    `json:"nickname,omitempty"` // mailNickname
	UPN          string    `json:"upn,omitempty"`
	Title        string    `json:"title,omitempty"`
	Department   string    `json:"department,omitempty"`
	Office       string    `json:"office,omitempty"`
	ManagerEmail string    `json:"managerEmail,omitempty"`
	ManagerName  string    `json:"managerName,omitempty"`
	Phones       []string  `json:"phones,omitempty"` // business phones
	Mobile       string    `json:"mobile,omitempty"`
	City         string    `json:"city,omitempty"`
	Country      string    `json:"country,omitempty"`
	HireDate     string    `json:"hireDate,omitempty"` // yyyy-mm-dd
	Missing      bool      `json:"missing,omitempty"`  // not in the directory (left, external, a list)
	FetchedAt    time.Time `json:"fetchedAt"`
	// V is the lookup version: entries from an older one are looked up
	// again (they lack fields added since).
	V int `json:"v,omitempty"`
}

// personVersion is the current lookup version (2: phones, city, country,
// hire date).
const personVersion = 2

// Directory is data/directory.json: the people Musubi has looked up.
type Directory struct {
	People map[string]*Person `json:"people"` // by lower-case email
}

// LoadDirectory reads the directory file; a missing file is empty.
func LoadDirectory(path string) (*Directory, error) {
	d := &Directory{People: map[string]*Person{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return d, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, d); err != nil {
		return nil, err
	}
	if d.People == nil {
		d.People = map[string]*Person{}
	}
	return d, nil
}

// Reports are the people whose manager is email, by name.
func (d *Directory) Reports(email string) []*Person {
	var out []*Person
	for _, p := range d.People {
		if strings.EqualFold(p.ManagerEmail, email) && !p.Missing {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out
}

// Chain is email's managers upward, nearest first, as far as the directory
// knows them (cycles stop it).
func (d *Directory) Chain(email string) []*Person {
	var out []*Person
	seen := map[string]bool{strings.ToLower(email): true}
	for p := d.People[strings.ToLower(email)]; p != nil && p.ManagerEmail != ""; {
		m := strings.ToLower(p.ManagerEmail)
		if seen[m] {
			break
		}
		seen[m] = true
		next := d.People[m]
		if next == nil {
			out = append(out, &Person{Email: m, Name: p.ManagerName})
			break
		}
		out = append(out, next)
		p = next
	}
	return out
}

// PeopleReport counts what a sync looked up.
type PeopleReport struct {
	Looked, Found, Missing, Left int
}

// SyncPeople looks up the given addresses in the directory (job title,
// department, office, manager) and writes them to path. An entry is looked
// up again after refresh; at most max are looked up per run, oldest first,
// so a first run over a big address book is spread across updates.
func SyncPeople(ctx context.Context, c *Client, path string, emails []string, refresh time.Duration, max int, now time.Time) (PeopleReport, error) {
	var rep PeopleReport
	d, err := LoadDirectory(path)
	if err != nil {
		return rep, err
	}
	type due struct {
		email string
		at    time.Time
	}
	var todo []due
	seen := map[string]bool{}
	for _, e := range emails {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" || seen[e] {
			continue
		}
		seen[e] = true
		p := d.People[e]
		if p == nil {
			todo = append(todo, due{e, time.Time{}})
		} else if now.Sub(p.FetchedAt) > refresh || p.V < personVersion && !p.Missing {
			todo = append(todo, due{e, p.FetchedAt})
		}
	}
	sort.Slice(todo, func(a, b int) bool {
		if !todo[a].at.Equal(todo[b].at) {
			return todo[a].at.Before(todo[b].at)
		}
		return todo[a].email < todo[b].email
	})
	if len(todo) > max {
		rep.Left = len(todo) - max
		todo = todo[:max]
	}
	dirty := false
	defer func() {
		if dirty {
			_, _ = WriteJSONIfChanged(path, d)
		}
	}()
	// A few lookups at once: each is two small requests, and one at a time
	// a first run took minutes.
	type result struct {
		email string
		p     *Person
		err   error
	}
	jobs := make(chan string)
	results := make(chan result)
	const workers = 6
	for w := 0; w < workers; w++ {
		go func() {
			for e := range jobs {
				p, err := lookUp(ctx, c, e)
				select {
				case results <- result{e, p, err}:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, t := range todo {
			select {
			case jobs <- t.email:
			case <-ctx.Done():
				return
			}
		}
	}()
	var firstErr error
	for range todo {
		var r result
		select {
		case r = <-results:
		case <-ctx.Done():
			return rep, ctx.Err()
		}
		if r.err != nil {
			if firstErr == nil {
				firstErr = r.err
			}
			continue
		}
		rep.Looked++
		r.p.FetchedAt, r.p.V = now, personVersion
		if r.p.Missing {
			rep.Missing++
		} else {
			rep.Found++
		}
		d.People[r.email] = r.p
		dirty = true
	}
	return rep, firstErr
}

func lookUp(ctx context.Context, c *Client, email string) (*Person, error) {
	var u struct {
		DisplayName       string   `json:"displayName"`
		GivenName         string   `json:"givenName"`
		Surname           string   `json:"surname"`
		Mail              string   `json:"mail"`
		MailNickname      string   `json:"mailNickname"`
		UserPrincipalName string   `json:"userPrincipalName"`
		JobTitle          string   `json:"jobTitle"`
		Department        string   `json:"department"`
		OfficeLocation    string   `json:"officeLocation"`
		AccountEnabled    *bool    `json:"accountEnabled"`
		BusinessPhones    []string `json:"businessPhones"`
		MobilePhone       string   `json:"mobilePhone"`
		City              string   `json:"city"`
		Country           string   `json:"country"`
		EmployeeHireDate  string   `json:"employeeHireDate"`
	}
	q := url.Values{"$select": {"displayName,givenName,surname,mail,mailNickname,userPrincipalName,jobTitle,department,officeLocation,accountEnabled,businessPhones,mobilePhone,city,country,employeeHireDate"}}
	err := c.GetJSON(ctx, "/v1.0/users/"+url.PathEscape(email)+"?"+q.Encode(), &u)
	if IsNotFound(err) || err == nil && u.AccountEnabled != nil && !*u.AccountEnabled {
		return &Person{Email: email, Missing: true}, nil
	}
	if err != nil {
		return nil, err
	}
	p := &Person{Email: email, Name: u.DisplayName, GivenName: u.GivenName, Surname: u.Surname, Nickname: u.MailNickname,
		UPN: strings.ToLower(u.UserPrincipalName), Title: u.JobTitle, Department: u.Department, Office: u.OfficeLocation,
		Phones: u.BusinessPhones, Mobile: u.MobilePhone, City: u.City, Country: u.Country}
	if len(u.EmployeeHireDate) >= 10 {
		p.HireDate = u.EmployeeHireDate[:10]
	}
	var m struct {
		DisplayName string `json:"displayName"`
		Mail        string `json:"mail"`
	}
	if err := c.GetJSON(ctx, "/v1.0/users/"+url.PathEscape(email)+"/manager?$select=displayName,mail", &m); err == nil {
		p.ManagerEmail, p.ManagerName = strings.ToLower(m.Mail), m.DisplayName
	} else if !IsNotFound(err) {
		return nil, err
	}
	return p, nil
}
