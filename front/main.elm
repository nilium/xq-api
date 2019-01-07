module Main exposing (main)

-- This experiment is for learning Elm, it is not the primary frontend to
-- xq-api. I haven't used Elm for anything other than this, so don't take this
-- as representative of the right way to do things. This is mostly for the sake
-- of prototyping something that isn't using Vue because Vue is, despite not
-- being as complicated as many other things (react and co.), still very
-- complex.
--
-- Does this mean Elm is better? Not necessarily. I'm not in a good position to
-- judge all the things anyone cares about being better. However, it's a lot
-- more pleasant to work with than.. well, everything else. So far, at least.
--
-- Anyway, this is a learning experiment, so don't count on quality here. The
-- goal is to get to the point where I'm more comfortable with the language.

import Browser
import Dict exposing (Dict)
import Html exposing (..)
import Html.Attributes exposing (..)
import Html.Events exposing (..)
import Http
import Json.Decode as Decode exposing (Decoder, field, int, list, string)
import Json.Decode.Pipeline exposing (optional, required)
import Maybe exposing (withDefault)
import Url.Builder as Url



-- Entrypoint


main =
    Browser.element
        { init = init
        , update = update
        , subscriptions = subscriptions
        , view = view
        }



-- Model
-- You can sort of see where this began as a tutorial thing:


type Archs
    = Failure
    | Loading
    | Success (List String)


type alias Model =
    { archs : Archs
    , arch : String
    , query : String
    , results : List QueryResult
    , lastError : Maybe Http.Error
    }


init : () -> ( Model, Cmd Msg )
init _ =
    ( { archs = Loading
      , arch = ""
      , query = ""
      , results = []
      , lastError = Nothing
      }
    , fetchArchs
    )



-- Update


type Msg
    = GotArchs (Result Http.Error (List String))
    | SelectArch String
    | InputQuery String
    | RunQuery
    | GotQueryResults (Result Http.Error (List QueryResult))


update : Msg -> Model -> ( Model, Cmd Msg )
update msg model =
    case msg of
        GotArchs (Ok archs) ->
            ( { model
                | archs = Success archs
                , arch = [ model.arch ] ++ archs |> coalesce
                , lastError = Nothing
              }
            , Cmd.none
            )

        GotArchs (Err err) ->
            ( { model
                | archs = Failure
                , lastError = Just err
              }
            , Cmd.none
            )

        SelectArch arch ->
            ( { model | arch = arch }
            , Cmd.none
            )

        InputQuery query ->
            ( { model | query = query }
            , Cmd.none
            )

        RunQuery ->
            ( model
            , fetchQueryRequest model.arch model.query
            )

        GotQueryResults (Ok results) ->
            ( { model
                | results = results
                , lastError = Nothing
              }
            , Cmd.none
            )

        GotQueryResults (Err err) ->
            ( { model
                | results = []
                , lastError = Just err
              }
            , Cmd.none
            )


coalesce : List String -> String
coalesce words =
    withDefault "" <| first (\x -> not <| String.isEmpty x) words


first : (a -> Bool) -> List a -> Maybe a
first select list =
    case list of
        h :: t ->
            if select h then
                Just h

            else
                first select t

        [] ->
            Nothing



-- Subscriptions


subscriptions : Model -> Sub Msg
subscriptions model =
    Sub.none



-- View


nothing : Html Msg
nothing =
    Html.text ""


view : Model -> Html Msg
view model =
    Html.div []
        [ h2 [] [ text "xq-api" ]

        -- Query form
        , viewQueryForm model

        -- Results (or error message)
        , case model.lastError of
            Just _ ->
                viewError model.lastError

            Nothing ->
                viewQueryTable model.results
        ]


viewQueryForm : Model -> Html Msg
viewQueryForm model =
    Html.form [ onSubmit RunQuery ]
        [ listArchs model
        , input
            [ type_ "text"
            , id "query"
            , placeholder "Query"
            , value model.query
            , onInput InputQuery
            ]
            []
        , button
            [ id "search"
            , type_ "submit"
            ]
            [ text "Search" ]
        ]



-- Error text


viewError : Maybe Http.Error -> Html Msg
viewError m =
    case m of
        Just _ ->
            div [] [ text <| errorText m ]

        Nothing ->
            nothing


errorText : Maybe Http.Error -> String
errorText m =
    case m of
        Nothing ->
            ""

        Just (Http.BadUrl s) ->
            "bad url: " ++ s

        Just Http.NetworkError ->
            "network error"

        Just Http.Timeout ->
            "timeout"

        Just (Http.BadStatus status) ->
            "invalid status code: " ++ String.fromInt status

        Just (Http.BadBody s) ->
            "bad body: " ++ s



-- Architecture selector


listArchs : Model -> Html Msg
listArchs model =
    case model.archs of
        Success archs ->
            archSelect archs

        Loading ->
            archSelect []

        Failure ->
            text "Failed."


archSelect : List String -> Html Msg
archSelect archs =
    Html.select [ id "arch", onInput SelectArch ] (List.map archListItem archs)


archListItem : String -> Html Msg
archListItem arch =
    Html.option [ value arch ] [ text arch ]



-- Query results table
-- TODO: This needs a pager.


viewQueryTable : List QueryResult -> Html Msg
viewQueryTable results =
    table [] ([ viewQueryTableHeader ] ++ List.map packageRow results)


viewQueryTableHeader : Html Msg
viewQueryTableHeader =
    tableRow th
        [ ( "hdr-name", "Name" )
        , ( "hdr-version", "Version" )
        , ( "hdr-desc", "Description" )
        ]


packageRow : QueryResult -> Html Msg
packageRow pkg =
    tableRow td
        [ ( "pkg-name", pkg.name )
        , ( "pkg-version", pkg.version ++ "_" ++ String.fromInt pkg.revision )
        , ( "pkg-desc", pkg.desc )
        ]


tableRow : (List (Attribute msg) -> List (Html msg) -> Html msg) -> List ( String, String ) -> Html msg
tableRow type_ columns =
    tr [] (List.map (tableCell type_) columns)


tableCell : (List (Attribute msg) -> List (Html msg) -> Html msg) -> ( String, String ) -> Html msg
tableCell type_ ( className, name ) =
    type_ [ class className ] [ text name ]



-- HTTP


archDecoder : Decoder (List String)
archDecoder =
    field "data" <| list string


fetchArchs : Cmd Msg
fetchArchs =
    Http.get
        { url = "http://127.0.0.1:8197/v1/archs"
        , expect = Http.expectJson GotArchs archDecoder
        }


fetchQuery : String -> String -> String
fetchQuery arch query =
    Url.relative [ "v1", "query", arch ] [ Url.string "q" query ]


fetchQueryRequest : String -> String -> Cmd Msg
fetchQueryRequest arch query =
    Http.get
        { url = "http://127.0.0.1:8197/" ++ fetchQuery arch query
        , expect = Http.expectJson GotQueryResults queryDecoder
        }


type alias QueryResult =
    { name : String
    , version : String
    , revision : Int
    , desc : String
    }


queryDecoder : Decoder (List QueryResult)
queryDecoder =
    field "data" <| list queryPackageDecoder


queryPackageDecoder : Decoder QueryResult
queryPackageDecoder =
    Decode.succeed QueryResult
        |> required "name" string
        |> required "version" string
        |> required "revision" int
        |> optional "desc" string ""
