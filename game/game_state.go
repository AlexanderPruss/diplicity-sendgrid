package game

import (
	"fmt"
	"io/ioutil"
	"net/http"

	"github.com/zond/diplicity/auth"
	"github.com/zond/godip"
	"github.com/zond/godip/variants"
	"golang.org/x/net/context"
	"google.golang.org/appengine"
	"google.golang.org/appengine/datastore"

	. "github.com/zond/goaeoas"
)

const (
	gameStateKind = "GameState"
)

var GameStateResource *Resource

func init() {
	GameStateResource = &Resource{
		Load:     loadGameState,
		Update:   updateGameState,
		FullPath: "/Game/{game_id}/GameState/{nation}",
		Listers: []Lister{
			{
				Path:    "/Game/{game_id}/GameStates",
				Route:   ListGameStatesRoute,
				Handler: listGameStates,
			},
		},
	}
}

type GameStates []GameState

func (g GameStates) Item(r Request, gameID *datastore.Key) *Item {
	gameStateItems := make(List, len(g))
	for i := range g {
		gameStateItems[i] = g[i].Item(r)
	}
	gameStatesItem := NewItem(gameStateItems).SetName("phase-states").AddLink(r.NewLink(Link{
		Rel:         "self",
		Route:       ListGameStatesRoute,
		RouteParams: []string{"game_id", gameID.Encode()},
	})).SetDesc([][]string{
		[]string{
			"Game states",
			"Each member has exactly one game state per game. The game state defines game scoped configuration for the member, such as which other members are muted in the chat.",
		},
		[]string{
			"Muting",
			"Adding another member nation to the 'Muted' list will hide all press from that member.",
			"Note that messages from muted members will still count towards the totals in the channel listings.",
		},
	})
	return gameStatesItem
}

type GameState struct {
	GameID *datastore.Key
	Nation godip.Nation
	Muted  []godip.Nation `methods:"PUT"`
}

func (g *GameState) HasMuted(nat godip.Nation) bool {
	for _, mut := range g.Muted {
		if mut == nat {
			return true
		}
	}
	return false
}

func GameStateID(ctx context.Context, gameID *datastore.Key, nation godip.Nation) (*datastore.Key, error) {
	if gameID == nil || nation == "" {
		return nil, fmt.Errorf("game states must have games and nations")
	}
	return datastore.NewKey(ctx, gameStateKind, string(nation), 0, gameID), nil
}

func (g *GameState) ID(ctx context.Context) (*datastore.Key, error) {
	return GameStateID(ctx, g.GameID, g.Nation)
}

func (g *GameState) Save(ctx context.Context) error {
	key, err := g.ID(ctx)
	if err != nil {
		return err
	}
	_, err = datastore.Put(ctx, key, g)
	return err
}

func (p *GameState) Item(r Request) *Item {
	gameStateItem := NewItem(p).SetName(string(p.Nation))
	memberNation, isMember := r.Values()[memberNationFlag]
	if isMember && memberNation == p.Nation {
		gameStateItem.AddLink(r.NewLink(GameStateResource.Link("update", Update, []string{"game_id", p.GameID.Encode(), "nation", fmt.Sprint(memberNation)})))
		gameStateItem.AddLink(r.NewLink(GameStateResource.Link("self", Load, []string{"game_id", p.GameID.Encode(), "nation", fmt.Sprint(memberNation)})))
	}
	return gameStateItem
}

func updateGameState(w ResponseWriter, r Request) (*GameState, error) {
	ctx := appengine.NewContext(r.Req())

	user, ok := r.Values()["user"].(*auth.User)
	if !ok {
		return nil, HTTPErr{"unauthenticated", http.StatusUnauthorized}
	}

	gameID, err := datastore.DecodeKey(r.Vars()["game_id"])
	if err != nil {
		return nil, err
	}

	nation := godip.Nation(r.Vars()["nation"])

	bodyBytes, err := ioutil.ReadAll(r.Req().Body)
	if err != nil {
		return nil, err
	}
	gameState := &GameState{}
	if err := datastore.RunInTransaction(ctx, func(ctx context.Context) error {
		game := &Game{}
		if err := datastore.Get(ctx, gameID, game); err != nil {
			return err
		}
		game.ID = gameID
		member, isMember := game.GetMemberByUserId(user.Id)
		if !isMember {
			return HTTPErr{"can only update phase state of member games", http.StatusNotFound}
		}

		if member.Nation != nation {
			return HTTPErr{"can only update own game state", http.StatusNotFound}
		}

		err = CopyBytes(gameState, r, bodyBytes, "PUT")
		if err != nil {
			return err
		}

		gameState.GameID = gameID
		gameState.Nation = member.Nation

		return gameState.Save(ctx)
	}, &datastore.TransactionOptions{XG: false}); err != nil {
		return nil, err
	}

	return gameState, nil
}

func loadGameState(w ResponseWriter, r Request) (*GameState, error) {
	ctx := appengine.NewContext(r.Req())

	user, ok := r.Values()["user"].(*auth.User)
	if !ok {
		return nil, HTTPErr{"unauthenticated", http.StatusUnauthorized}
	}

	gameID, err := datastore.DecodeKey(r.Vars()["game_id"])
	if err != nil {
		return nil, err
	}

	nation := godip.Nation(r.Vars()["nation"])

	gameStateID, err := GameStateID(ctx, gameID, nation)
	if err != nil {
		return nil, err
	}

	game := &Game{}
	gameState := &GameState{}
	err = datastore.GetMulti(ctx, []*datastore.Key{gameID, gameStateID}, []interface{}{game, gameState})
	if err != nil {
		if merr, ok := err.(appengine.MultiError); ok {
			if merr[0] != nil {
				return nil, err
			} else if merr[1] == datastore.ErrNoSuchEntity {
				gameState.GameID = gameID
				gameState.Nation = nation
			} else if merr[1] != nil {
				return nil, err
			}
		} else {
			return nil, err
		}
	}
	game.ID = gameID

	if !game.Mustered {
		gameState.Nation = ""
	}

	member, isMember := game.GetMemberByUserId(user.Id)
	if isMember {
		r.Values()[memberNationFlag] = member.Nation
	}

	return gameState, nil
}

func listGameStates(w ResponseWriter, r Request) error {
	ctx := appengine.NewContext(r.Req())

	user, ok := r.Values()["user"].(*auth.User)
	if !ok {
		return HTTPErr{"unauthenticated", http.StatusUnauthorized}
	}

	gameID, err := datastore.DecodeKey(r.Vars()["game_id"])
	if err != nil {
		return err
	}

	game := &Game{}
	if err = datastore.Get(ctx, gameID, game); err != nil {
		return err
	}

	member, isMember := game.GetMemberByUserId(user.Id)
	if isMember {
		r.Values()[memberNationFlag] = member.Nation
	}

	gameStates := GameStates{}

	if _, err := datastore.NewQuery(gameStateKind).Ancestor(gameID).GetAll(ctx, &gameStates); err != nil {
		return err
	}
	for _, nat := range variants.Variants[game.Variant].Nations {
		found := false
		for _, gameState := range gameStates {
			if gameState.Nation == nat {
				found = true
				break
			}
		}
		if !found {
			gameStates = append(gameStates, GameState{
				GameID: gameID,
				Nation: nat,
			})
		}
	}

	if !game.Mustered {
		for idx := range gameStates {
			gameStates[idx].Nation = ""
		}
	}

	w.SetContent(gameStates.Item(r, gameID))
	return nil
}
