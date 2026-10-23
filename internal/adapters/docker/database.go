package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/rupaai/homelab/internal/core"
)

// resetSchemas empties the database: every schema goes (the system ones stay) and an empty
// public one is made, so a restore leaves nothing that wasn't in the dump.
const resetSchemas = `DO $$ DECLARE s text; BEGIN ` +
	`FOR s IN SELECT nspname FROM pg_namespace WHERE nspname NOT LIKE 'pg\_%' AND nspname <> 'information_schema' ` +
	`LOOP EXECUTE format('DROP SCHEMA %I CASCADE', s); END LOOP; CREATE SCHEMA public; END $$`

// dbExec runs a command inside the environment's database container with `docker compose
// exec -T`, so nothing but Homelab's own containers is reached. stdout goes to out, and
// stdin comes from in; both may be nil.
func (d *Deployer) dbExec(ctx context.Context, app, env string, in io.Reader, out io.Writer, args ...string) error {
	if err := checkNames(app, env); err != nil {
		return err
	}
	cs, err := d.api.containers(ctx, labelApp+"="+app, labelEnv+"="+env, labelService+"="+core.DatabaseService)
	if err != nil {
		return err
	}
	running := false
	for _, c := range cs {
		running = running || c.State == "running"
	}
	if !running {
		return fmt.Errorf("%s/%s has no running database", app, env)
	}
	full := append([]string{"exec", "-T", core.DatabaseService}, args...)
	cmd, err := d.composeCmd(ctx, d.envDir(app, env), project(app, env), full...)
	if err != nil {
		return err
	}
	msg := &tailBuffer{max: maxLogBytes}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = in, out, msg
	if out == nil {
		cmd.Stdout = msg
	}
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%s failed for %s/%s: %w\n%s", args[0], app, env, err, lastLines(msg.Bytes(), errTailLines))
	}
	return nil
}

// DumpDatabase writes `pg_dump -Fc` of the environment's database to file. The file is
// written under another name first, so a failed dump leaves nothing behind.
func (d *Deployer) DumpDatabase(ctx context.Context, app, env, file string) error {
	if _, err := os.Lstat(file); err == nil {
		return fmt.Errorf("%s already exists", file)
	}
	tmp := file + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	err = d.dbExec(ctx, app, env, nil, f, "pg_dump", "-U", core.DatabaseUser, "-d", core.DatabaseName, "-Fc")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, file)
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// RestoreDatabase empties the environment's database and loads the dump into it.
func (d *Deployer) RestoreDatabase(ctx context.Context, app, env, file string) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() {
		return errors.New(filepath.Base(file) + " isn't a dump file")
	}
	if err := d.dbExec(ctx, app, env, nil, nil, "psql", "-U", core.DatabaseUser, "-d", core.DatabaseName, "-v", "ON_ERROR_STOP=1", "-c", resetSchemas); err != nil {
		return err
	}
	return d.dbExec(ctx, app, env, f, nil, "pg_restore", "-U", core.DatabaseUser, "-d", core.DatabaseName, "--clean", "--if-exists", "--no-owner")
}
